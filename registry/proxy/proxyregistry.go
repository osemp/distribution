package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/docker/distribution"
	"github.com/docker/distribution/configuration"
	dcontext "github.com/docker/distribution/context"
	"github.com/docker/distribution/reference"
	"github.com/docker/distribution/registry/client"
	"github.com/docker/distribution/registry/client/auth"
	"github.com/docker/distribution/registry/client/auth/challenge"
	"github.com/docker/distribution/registry/client/transport"
	"github.com/docker/distribution/registry/proxy/scheduler"
	"github.com/docker/distribution/registry/storage"
	"github.com/docker/distribution/registry/storage/driver"
)

var (
	DefaultV2Registry = &url.URL{
		Scheme: "https",
		Host:   "registry-1.docker.io",
	}
)

// proxyingRegistry fetches content from a remote registry and caches it locally
type proxyingRegistry struct {
	embedded           distribution.Namespace // provides local registry functionality
	scheduler          *scheduler.TTLExpirationScheduler
	remoteURL          url.URL
	authChallenger     authChallenger
	authChallengers    map[string]authChallenger
	authChallengersMux sync.RWMutex
}

// NewRegistryPullThroughCache creates a registry acting as a pull through cache
func NewRegistryPullThroughCache(ctx context.Context, registry distribution.Namespace, driver driver.StorageDriver, config configuration.Proxy) (distribution.Namespace, error) {
	remoteURL, err := url.Parse(config.RemoteURL)
	if err != nil {
		return nil, err
	}

	v := storage.NewVacuum(ctx, driver)
	s := scheduler.New(ctx, driver, "/scheduler-state.json")
	s.OnBlobExpire(func(ref reference.Reference) error {
		var r reference.Canonical
		var ok bool
		if r, ok = ref.(reference.Canonical); !ok {
			return fmt.Errorf("unexpected reference type : %T", ref)
		}

		repo, err := registry.Repository(ctx, r)
		if err != nil {
			return err
		}

		blobs := repo.Blobs(ctx)

		// Clear the repository reference and descriptor caches
		err = blobs.Delete(ctx, r.Digest())
		if err != nil {
			return err
		}

		err = v.RemoveBlob(r.Digest().String())
		if err != nil {
			return err
		}

		return nil
	})

	s.OnManifestExpire(func(ref reference.Reference) error {
		var r reference.Canonical
		var ok bool
		if r, ok = ref.(reference.Canonical); !ok {
			return fmt.Errorf("unexpected reference type : %T", ref)
		}

		repo, err := registry.Repository(ctx, r)
		if err != nil {
			return err
		}

		manifests, err := repo.Manifests(ctx)
		if err != nil {
			return err
		}
		err = manifests.Delete(ctx, r.Digest())
		if err != nil {
			return err
		}
		return nil
	})

	err = s.Start()
	if err != nil {
		return nil, err
	}

	cs, err := configureAuth(config.Username, config.Password, config.RemoteURL)
	if err != nil {
		return nil, err
	}

	return &proxyingRegistry{
		embedded:  registry,
		scheduler: s,
		remoteURL: *remoteURL,
		authChallenger: &remoteAuthChallenger{
			remoteURL: *remoteURL,
			cm:        challenge.NewSimpleManager(),
			cs:        cs,
		},
	}, nil
}

func NewRegistryPullThroughCacheOnRemoteRegistries(ctx context.Context, registry distribution.Namespace, driver driver.StorageDriver, remoteRegistries []configuration.RemoteRegistry) (distribution.Namespace, error) {
	type remote struct {
		url                      *url.URL
		rawUrl, username, passwd string
	}
	var remotes []remote
	for _, remoteRegistry := range remoteRegistries {
		remoteURL, err := url.Parse(remoteRegistry.URL)
		if err != nil {
			return nil, err
		}
		// TODO maybe retrieve username/password from ~/.docker/config.json
		remotes = append(remotes, remote{
			url:      remoteURL,
			rawUrl:   remoteRegistry.URL,
			username: remoteRegistry.Username,
			passwd:   remoteRegistry.Password,
		})
	}

	v := storage.NewVacuum(ctx, driver)
	s := scheduler.New(ctx, driver, "/scheduler-state.json")
	s.OnBlobExpire(func(ref reference.Reference) error {
		var r reference.Canonical
		var ok bool
		if r, ok = ref.(reference.Canonical); !ok {
			return fmt.Errorf("unexpected reference type : %T", ref)
		}

		repo, err := registry.Repository(ctx, r)
		if err != nil {
			return err
		}

		blobs := repo.Blobs(ctx)

		// Clear the repository reference and descriptor caches
		err = blobs.Delete(ctx, r.Digest())
		if err != nil {
			return err
		}

		err = v.RemoveBlob(r.Digest().String())
		if err != nil {
			return err
		}

		return nil
	})

	s.OnManifestExpire(func(ref reference.Reference) error {
		var r reference.Canonical
		var ok bool
		if r, ok = ref.(reference.Canonical); !ok {
			return fmt.Errorf("unexpected reference type : %T", ref)
		}

		repo, err := registry.Repository(ctx, r)
		if err != nil {
			return err
		}

		manifests, err := repo.Manifests(ctx)
		if err != nil {
			return err
		}
		err = manifests.Delete(ctx, r.Digest())
		if err != nil {
			return err
		}
		return nil
	})

	err := s.Start()
	if err != nil {
		return nil, err
	}

	remoteAuthChallengers := map[string]authChallenger{}
	for _, rmt := range remotes {
		cs, err := configureAuth(rmt.username, rmt.passwd, rmt.rawUrl)
		if err != nil {
			return nil, err
		}
		remoteAuthChallengers[rmt.url.Host] = &remoteAuthChallenger{
			remoteURL: *rmt.url,
			cm:        challenge.NewSimpleManager(),
			cs:        cs,
		}
	}

	return &proxyingRegistry{
		embedded:        registry,
		scheduler:       s,
		authChallengers: remoteAuthChallengers,
	}, nil
}

func NewRegistryPullThroughWithNothing(ctx context.Context, registry distribution.Namespace, driver driver.StorageDriver) (distribution.Namespace, error) {
	remoteAuthChallengers := map[string]authChallenger{}
	// give a empty scheduler
	s := scheduler.New(ctx, driver, "/scheduler-state.json")
	return &proxyingRegistry{
		embedded:        registry,
		scheduler:       s,
		authChallengers: remoteAuthChallengers,
	}, nil
}

func (pr *proxyingRegistry) Scope() distribution.Scope {
	return distribution.GlobalScope
}

func (pr *proxyingRegistry) Repositories(ctx context.Context, repos []string, last string) (n int, err error) {
	return pr.embedded.Repositories(ctx, repos, last)
}

func (pr *proxyingRegistry) addChallengerDynamically(authConfig AuthConfig, endpoint *url.URL) (*remoteAuthChallenger, error) {
	var (
		err  error
		sURL *url.URL
		cs   auth.CredentialStore
		c    *remoteAuthChallenger
	)

	if !strings.HasPrefix(authConfig.ServerAddress, "https://") && !strings.HasPrefix(authConfig.ServerAddress, "http://") {
		authConfig.ServerAddress = "https://" + authConfig.ServerAddress
	}

	sURL, err = url.Parse(authConfig.ServerAddress)
	if err != nil {
		return nil, err
	}

	cs, err = configureAuth(authConfig.Username, authConfig.Password, authConfig.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to config auth for %s", authConfig.ServerAddress)
	}

	c = &remoteAuthChallenger{
		remoteURL: *sURL,
		cm:        challenge.NewSimpleManager(),
		cs:        cs,
	}
	//TODO consider if it's necessary to sync this config into config file
	//pr.authChallengersMux.Lock()
	//pr.authChallengers[endpoint.Host] = c
	//pr.authChallengersMux.Unlock()
	return c, nil
}

func (pr *proxyingRegistry) Repository(ctx context.Context, name reference.Named) (distribution.Repository, error) {
	var (
		c         = pr.authChallenger
		remoteUrl = pr.remoteURL
		err       error
	)

	c, remoteUrl, err = pr.getChallengerFromMirrorHeader(ctx, c, remoteUrl)
	if err != nil {
		logrus.Warnf("failed to get challenger, try to use default challenger, err: %s", err)
		// always return a default challenger, just to make sure the local registry could work
		c = pr.defaultChallenger()
		remoteUrl = *DefaultV2Registry
	}

	tkopts := auth.TokenHandlerOptions{
		Transport:   http.DefaultTransport,
		Credentials: c.credentialStore(),
		Scopes: []auth.Scope{
			auth.RepositoryScope{
				Repository: name.Name(),
				Actions:    []string{"pull"},
			},
		},
		Logger: dcontext.GetLogger(ctx),
	}

	tr := transport.NewTransport(http.DefaultTransport,
		auth.NewAuthorizer(c.challengeManager(),
			auth.NewTokenHandlerWithOptions(tkopts)))

	localRepo, err := pr.embedded.Repository(ctx, name)
	if err != nil {
		return nil, err
	}
	localManifests, err := localRepo.Manifests(ctx, storage.SkipLayerVerification())
	if err != nil {
		return nil, err
	}

	remoteRepo, err := client.NewRepository(name, remoteUrl.String(), tr)
	if err != nil {
		return nil, err
	}

	remoteManifests, err := remoteRepo.Manifests(ctx)
	if err != nil {
		return nil, err
	}

	return &proxiedRepository{
		blobStore: &proxyBlobStore{
			localStore:     localRepo.Blobs(ctx),
			remoteStore:    remoteRepo.Blobs(ctx),
			scheduler:      pr.scheduler,
			repositoryName: name,
			authChallenger: c,
		},
		manifests: &proxyManifestStore{
			repositoryName:  name,
			localManifests:  localManifests, // Options?
			remoteManifests: remoteManifests,
			ctx:             ctx,
			scheduler:       pr.scheduler,
			authChallenger:  c,
		},
		name: name,
		tags: &proxyTagService{
			localTags:      localRepo.Tags(ctx),
			remoteTags:     remoteRepo.Tags(ctx),
			authChallenger: c,
		},
	}, nil
}

func (pr *proxyingRegistry) Blobs() distribution.BlobEnumerator {
	return pr.embedded.Blobs()
}

func (pr *proxyingRegistry) BlobStatter() distribution.BlobStatter {
	return pr.embedded.BlobStatter()
}

func parseMirrorOriginalDomain(endp string) (*url.URL, error) {
	if !strings.HasPrefix(endp, "https://") && !strings.HasPrefix(endp, "http://") {
		endp = "https://" + endp
	}

	return url.Parse(endp)
}

func (pr *proxyingRegistry) getChallengerFromMirrorHeader(ctx context.Context, c authChallenger, rurl url.URL) (authChallenger, url.URL, error) {
	// remoteUrl is not set, the option remoteregistries is configured definitely.
	// Otherwise, the func (pr *proxyingRegistry) Repository won't be called.
	// So, no need to check the len for challengers
	if c != nil {
		return c, rurl, nil
	}

	req, err := dcontext.GetRequest(ctx)
	if err != nil {
		return nil, url.URL{}, err
	}
	// domain comes from docker daemon pull request
	// hacked docker pull a.hub/ns/nginx:latest will add https/http://a.hub to the header of pull request
	// so we can know where is the original domain for pull request
	endp := req.Header.Get("domain")
	if endp == "" {
		// just return, outside will return a default repository
		return nil, url.URL{}, errors.New("failed to get repository, err: domain is empty")
	}
	endpoint, err := parseMirrorOriginalDomain(endp)
	if err != nil {
		return nil, url.URL{}, fmt.Errorf("failed to parse mirror-original-domain %s to url, err: %s", endp, err)
	}
	pr.authChallengersMux.RLock()
	c = pr.authChallengers[endpoint.Host]
	pr.authChallengersMux.RUnlock()
	if c == nil {
		authConfig := req.Header.Get("domain-auth")
		dst, err := base64.StdEncoding.DecodeString(authConfig)
		if err != nil {
			return nil, url.URL{}, fmt.Errorf("failed to decode authConfig in base64")
		}

		dAuthConfig := AuthConfig{}
		err = json.Unmarshal(dst, &dAuthConfig)
		if err != nil {
			return nil, url.URL{}, fmt.Errorf("failed to unmarshal authconfig %v, err: %s", authConfig, err)
		}
		// get from header if challenger is empty
		// in sealer-hacked docker, this filed may same as "domain" header
		if dAuthConfig.ServerAddress == "" {
			c, err = pr.defaultChallengerForCertainServer(endpoint)
			if err != nil {
				return nil, url.URL{}, fmt.Errorf("failed to find get default chanllengers for %s, err: %s", endp, err)
			}
		} else {
			c, err = pr.addChallengerDynamically(dAuthConfig, endpoint)
			if err != nil {
				return nil, url.URL{}, err
			}
		}
	}
	// won't happen
	rac, ok := c.(*remoteAuthChallenger)
	if !ok {
		return nil, url.URL{}, errors.New("failed to transform proxy registry authChallenger to remoteAuthChallenger")
	}
	return c, rac.remoteURL, nil
}

func (pr *proxyingRegistry) defaultChallenger() *remoteAuthChallenger {
	pr.authChallengersMux.RLock()
	c := pr.authChallengers[DefaultV2Registry.Host]
	pr.authChallengersMux.RUnlock()
	if c != nil {
		return c.(*remoteAuthChallenger)
	}

	cs, err := configureAuth("", "", DefaultV2Registry.String())
	if err != nil {
		logrus.Warnf("failed to config auth for %s, give a final default challenger", DefaultV2Registry.String())
		// do not store the challenger
		return &remoteAuthChallenger{
			remoteURL: *DefaultV2Registry,
			cm:        challenge.NewSimpleManager(),
			cs:        credentials{creds: map[string]userpass{}},
		}
	}
	// do not store the challenger
	return &remoteAuthChallenger{
		remoteURL: *DefaultV2Registry,
		cm:        challenge.NewSimpleManager(),
		cs:        cs,
	}
}

func (pr *proxyingRegistry) defaultChallengerForCertainServer(endpoint *url.URL) (*remoteAuthChallenger, error) {
	cs, err := configureAuth("", "", endpoint.String())
	if err != nil {
		return nil, fmt.Errorf("failed to config auth for %s", endpoint.String())
	}
	// do not store the challenger
	return &remoteAuthChallenger{
		remoteURL: *endpoint,
		cm:        challenge.NewSimpleManager(),
		cs:        cs,
	}, nil
}

// authChallenger encapsulates a request to the upstream to establish credential challenges
type authChallenger interface {
	tryEstablishChallenges(context.Context) error
	challengeManager() challenge.Manager
	credentialStore() auth.CredentialStore
}

type remoteAuthChallenger struct {
	remoteURL url.URL
	sync.Mutex
	cm challenge.Manager
	cs auth.CredentialStore
}

func (r *remoteAuthChallenger) credentialStore() auth.CredentialStore {
	return r.cs
}

func (r *remoteAuthChallenger) challengeManager() challenge.Manager {
	return r.cm
}

// tryEstablishChallenges will attempt to get a challenge type for the upstream if none currently exist
func (r *remoteAuthChallenger) tryEstablishChallenges(ctx context.Context) error {
	r.Lock()
	defer r.Unlock()

	remoteURL := r.remoteURL
	remoteURL.Path = "/v2/"
	challenges, err := r.cm.GetChallenges(remoteURL)
	if err != nil {
		return err
	}

	if len(challenges) > 0 {
		return nil
	}

	// establish challenge type with upstream
	if err := ping(r.cm, remoteURL.String(), challengeHeader); err != nil {
		return err
	}

	dcontext.GetLogger(ctx).Infof("Challenge established with upstream : %s %s", remoteURL, r.cm)
	return nil
}

// proxiedRepository uses proxying blob and manifest services to serve content
// locally, or pulling it through from a remote and caching it locally if it doesn't
// already exist
type proxiedRepository struct {
	blobStore distribution.BlobStore
	manifests distribution.ManifestService
	name      reference.Named
	tags      distribution.TagService
}

func (pr *proxiedRepository) Manifests(ctx context.Context, options ...distribution.ManifestServiceOption) (distribution.ManifestService, error) {
	return pr.manifests, nil
}

func (pr *proxiedRepository) Blobs(ctx context.Context) distribution.BlobStore {
	return pr.blobStore
}

func (pr *proxiedRepository) Named() reference.Named {
	return pr.name
}

func (pr *proxiedRepository) Tags(ctx context.Context) distribution.TagService {
	return pr.tags
}
