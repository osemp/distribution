### osemp-distribution 源码解析

本文主要介绍sealer中内置的distribution(registry)是如何改造，以实现动态配置认证信息，以及多远程仓库缓存的。

##### 名次解释
* Repository: 在我刚结束镜像仓库的时候，很容易把Registry与Repository搞混，总认为其实是一个东西，但实际并不是一回事。举个具体的例子描述，镜像仓库缓存名为`sealer.hub/sealer/nginx:v1`, 那么该镜像缓存到镜像仓库，会被放到名为`sealer`的repository下。对应镜像仓库的缓存地址就是`/var/lib/registry/docker/registry/v2/repositories/sealer/nginx/`

* Registry/Namespace: 源码中这两个都是接口，源码中具体的registry对象也是实现了Namespace和Registry接口，源码中对Namespace的解释相当直观，就是repositories的集合。

##### distribution 启动
distribution启动的逻辑在registry/handlers/app.go里，我们只用关心NewApp(....)这个函数。
```go
func NewApp(ctx context.Context, config *configuration.Configuration) *App {
	app := &App{
		Config:  config,
		Context: ctx,
		router:  v2.RouterWithPrefix(config.HTTP.Prefix),
		isCache: (config.Proxy.RemoteURL != "" || len(config.Proxy.RemoteRegistries) != 0) && config.Proxy.On,
	}
    
    // step 1
    // 首先注册了请求路由，以及路由对应的处理handler。
	app.register(v2.RouteNameBase, func(ctx *Context, r *http.Request) http.Handler {
		return http.HandlerFunc(apiBase)
	})
	app.register(v2.RouteNameManifest, manifestDispatcher)
	app.register(v2.RouteNameCatalog, catalogDispatcher)
	app.register(v2.RouteNameTags, tagsDispatcher)
	app.register(v2.RouteNameBlob, blobDispatcher)
	app.register(v2.RouteNameBlobUpload, blobUploadDispatcher)
	app.register(v2.RouteNameBlobUploadChunk, blobUploadDispatcher)
    // step 2
    // 设置registry的cache能力，cache主要是用来避免多次查找blob地址的，
    // 命中后，会直接从cache中返回blob的地址。
    // 通常我们默认会配置inmemory形式的registry。
	if cc, ok := config.Storage["cache"]; ok {
    ...
		switch v {
		case "redis":
        ...
		case "inmemory":
			cacheProvider := memorycache.NewInMemoryBlobDescriptorCacheProvider()
			localOptions := append(options, storage.BlobDescriptorCacheProvider(cacheProvider))
			app.registry, err = storage.NewRegistry(app, app.driver, localOptions...)
			if err != nil {
				panic("could not create registry: " + err.Error())
			}
			dcontext.GetLogger(app).Infof("using inmemory blob descriptor cache")
			}
		}
	}
}
    // 到这里，app.registry已经被初始化，不过registry时本地registry，可以返回本地已存储的镜像。
    // 但是还并没有cache的能力。
    
    // step 3
    // 配置cache registry，如果yaml里填了具体的配置
	if config.Proxy.RemoteURL != "" {
		app.registry, err = proxy.NewRegistryPullThroughCache(ctx, app.registry, app.driver, config.Proxy)
		if err != nil {
			panic(err.Error())
		}
		app.isCache = true
		dcontext.GetLogger(app).Info("Registry configured as a proxy cache to ", config.Proxy.RemoteURL)
	} else if config.Proxy.On {
    // Proxy.On的开关是sealer定制的，目的是满足用户不需要提前配置远程镜像仓库的认证信息。
		if len(config.Proxy.RemoteRegistries) == 0 {
			app.registry, err = proxy.NewRegistryPullThroughWithNothing(ctx, app.registry, app.driver)
			if err != nil {
				panic(err.Error())
			}
			dcontext.GetLogger(app).Info("Registry configured as a registry with nothing pre-configs")
		} else {
			app.registry, err = proxy.NewRegistryPullThroughCacheOnRemoteRegistries(ctx, app.registry, app.driver, config.Proxy.RemoteRegistries)
			if err != nil {
				panic(err.Error())
			}
			var regs []string
			for _, reg := range config.Proxy.RemoteRegistries {
				regs = append(regs, reg.URL)
			}
			dcontext.GetLogger(app).Info("Registry configured as a proxy cache to ", regs)
		}
		app.isCache = true
	}
    
```

``` go
 // 上面创建cache registry需要注意点的是，我们总是会传入app.registry这个对象。
 // 目的是用起提供本地的registry能力，无论是NewRegistryPullThroughCacheOnRemoteRegistries/NewRegistryPullThroughWithNothing/NewRegistryPullThroughCache，最后都会返回一个proxyingRegistry对象，其embedded封装了本地registry。
type proxyingRegistry struct {
	embedded           distribution.Namespace // provides local registry functionality
	scheduler          *scheduler.TTLExpirationScheduler
	remoteURL          url.URL
	authChallenger     authChallenger
	authChallengers    map[string]authChallenger
	authChallengersMux sync.RWMutex
}
```

##### NewRegistryPullThroughCacheOnRemoteRegistries 返回缓存registry

其实distribution很久之前就提供了镜像缓存的能力了，只是可能由于docker不开放除dockerhub之外的mirror，所以distribution也仅仅支持一个远程镜像仓库作为缓存的配置。
所以我们做的事情只是基于原来的能力，打破这个限制。
返回看看proxyingRegistry的结构，其中原生带了的字段是`remoteURL`， `authChallenger`，这两个分别是原生yaml中提供配置的remoteURL，以及该URL对应的认证通道，为了解除这样的限制，我们添加了`authChallengers    map[string]authChallenger`。
所以在返回registry这里，我们做的最主要的事情，就是配置多个remoteURL与其对应的authChallenger，其对应配置文件中的：
```yaml
proxy:
  remoteregistries:
  # will cache image from docker pull docker.io/library/nginx:latest or docker pull nginx
  - url: https://registry-1.docker.io #dockerhub default registry
    username:
    password:
    # will cache image from docker pull reg.test1.com/library/nginx:latest
  - url: https://reg.test1.com
    username: username
    password: password
  - url: http://reg.test2.com
    username: username
    password: password
```

```go
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

		remotes = append(remotes, remote{
			url:      remoteURL,
			rawUrl:   remoteRegistry.URL,
			username: remoteRegistry.Username,
			passwd:   remoteRegistry.Password,
		})
	}
    ...
    //省略scheduler，做的工作是镜像清理的，也许我们应该考虑把这个逻辑去掉。
    ...
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
```

上面主要展示的是registry是如何被创建以及启动的，registry的作用就是返回repository资源。而具体的manifest/blob再依靠repository返回。
每次docker的请求到来，都会dispatch到具体的http.Handler，具体逻辑在函数app.go 的 dispatcher中：
```go
func (app *App) dispatcher(dispatch dispatchFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for headerName, headerValues := range app.Config.HTTP.Headers {
			for _, value := range headerValues {
				w.Header().Add(headerName, value)
			}
		}

		context := app.context(w, r)

		if err := app.authorized(w, r, context); err != nil {
			dcontext.GetLogger(context).Warnf("error authorizing context: %v", err)
			return
		}

		// Add username to request logging
		context.Context = dcontext.WithLogger(context.Context, dcontext.GetLogger(context.Context, auth.UserNameKey))
		// sync up context on the request.
		r = r.WithContext(context)

		if app.nameRequired(r) {
			nameRef, err := reference.WithName(getName(context))
			if err != nil {
				dcontext.GetLogger(context).Errorf("error parsing reference from context: %v", err)
				context.Errors = append(context.Errors, distribution.ErrRepositoryNameInvalid{
					Name:   getName(context),
					Reason: err,
				})
				if err := errcode.ServeJSON(w, context.Errors); err != nil {
					dcontext.GetLogger(context).Errorf("error serving error json: %v (from %v)", err, context.Errors)
				}
				return
			}
            // 关键在这里，返回了具体的repository。
			repository, err := app.registry.Repository(context, nameRef)

		dispatch(context, r).ServeHTTP(w, r)
		// Automated error response handling here. Handlers may return their
		// own errors if they need different behavior (such as range errors
		// for layer upload).
		if context.Errors.Len() > 0 {
			if err := errcode.ServeJSON(w, context.Errors); err != nil {
				dcontext.GetLogger(context).Errorf("error serving error json: %v (from %v)", err, context.Errors)
			}

			app.logError(context, context.Errors)
		}
	})
}
```
dispatcher函数最关键的就是返回了具体的repository，依靠该repository，distribution能通过具体路由信息，返回repository下的manifest，blob等信息。

``` go
func (pr *proxyingRegistry) Repository(ctx context.Context, name reference.Named) (distribution.Repository, error) {
	var (
    // 该函数关键的是，设置正确的c，remoteUrl，这两个值会影响缓存具体去哪个远程仓库拉取缓存。
    // 在上面我们说过了，remoteURL是registry原生的配置，只支持一个远程镜像仓库的配置
    // 那为何这里还设置成他们呢？
    // 因为这是为了保持原来distribution的逻辑，就是说如果用户配置了distribution的原生配置，而不是sealer提供的配置，仍然能让缓存能力有效。
		c         = pr.authChallenger
		remoteUrl = pr.remoteURL
		err       error
	)

    // 这里返回了我们最终需要的remoteUrl和challenger。
	c, remoteUrl, err = pr.getChallengerFromMirrorHeader(ctx, c, remoteUrl)
	if err != nil {
		logrus.Warnf("failed to get challenger, err: %s", err)
        // 这里有个degradeToLocal方法，就是当我们无法获取远程镜像仓库头部信息，或者获取远程token失败（比如断网环境）以后，会把repository降为本地repository，以提供本地的镜像能力。
		return pr.degradeToLocal(ctx, name)
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

    // 提供本地repository
	localRepo, err := pr.embedded.Repository(ctx, name)
	if err != nil {
		return nil, err
	}
	localManifests, err := localRepo.Manifests(ctx, storage.SkipLayerVerification())
	if err != nil {
		return nil, err
	}
    // 顾名思义，缓存时，主要靠这个repository发送请求。
	remoteRepo, err := client.NewRepository(name, remoteUrl.String(), tr)
	if err != nil {
		return nil, err
	}

	remoteManifests, err := remoteRepo.Manifests(ctx)
	if err != nil {
		return nil, err
	}

	localStore := localRepo.Blobs(ctx)
	return &proxiedRepository{
		blobStore: &proxyBlobStore{
			localStore:     localStore,
			remoteStore:    remoteRepo.Blobs(ctx),
			scheduler:      pr.scheduler,
			repositoryName: name,
			authChallenger: c,
		},
		manifests: &proxyManifestStore{
			repositoryName:  name,
			localManifests:  localManifests,
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
```
另外要特别提的是，registry提供的服务是repository scope的，就是说假如repository A与repository B分别有两个镜像image ra, image rb，这两个镜像共享了blob C，但是想想，如果我们利用缓存的能力的话，`docker pull ra`后再`docker pull rb`，对rb来说。因为共享blob C，所以docker不会再发起对blob C的拉取请求，这就导致，在repository，blob C并没有缓存在repository B下，但是registry全局来看是有的，只不过存在了repository A。这时我们删掉两个镜像，重启registry(清空cache)，`docker pull rb`，会导致无法拉取镜像。除非我们先拉取ra，因为其能够在inmemory cache中更新blob C的地址，所以即使repository B中没有blob C，也能拉取到。但是rb无法被正常拉取的风险仍然在，所以sealer还做了其他改动，在registry启动时，更新cache（其实我觉得这种方式实在不好，我更青睐于，用scheduler，定时在repository之间进行blob同步，给用户提供入口，选择是否开启，但是时间不够，就直接刷进cache里）。这部分逻辑如下：
```go
func BlobDescriptorCacheProvider(blobDescriptorCacheProvider cache.BlobDescriptorCacheProvider) RegistryOption {
	// TODO(aaronl): The duplication of statter across several objects is
	// ugly, and prevents us from using interface types in the registry
	// struct. Ideally, blobStore and blobServer should be lazily
	// initialized, and use the current value of
	// blobDescriptorCacheProvider.
	return func(registry *registry) error {
		if blobDescriptorCacheProvider != nil {
			//previousStatter := registry.statter
			descriptorSeen := map[string]distribution.Descriptor{}
			type repoBDCP struct {
				bdcp     distribution.BlobDescriptorService
				layers   []string
				repoName string
			}
			repoBDCPs := []repoBDCP{}

			statter := cache.NewCachedBlobStatter(blobDescriptorCacheProvider, registry.statter)
			registry.blobStore.statter = statter
			registry.blobServer.statter = statter
			registry.blobDescriptorCacheProvider = blobDescriptorCacheProvider
            //遍历整个registry。
			registry.Enumerate(context.Background(), func(repoName string) error {
				var (
					err error
					ctx = context.Background()
				)

				bdcp, err := blobDescriptorCacheProvider.RepositoryScoped(repoName)
				if err != nil {
					return err
				}

				repobdcp := repoBDCP{bdcp: bdcp, repoName: repoName}
				named, err := reference.WithName(repoName)
				if err != nil {
					return fmt.Errorf("failed to parse repo name %s: %v", repoName, err)
				}
				repo, err := registry.Repository(ctx, named)
				if err != nil {
					return fmt.Errorf("failed to construct repository: %v", err)
				}

				manifestService, err := repo.Manifests(ctx)
				if err != nil {
					return fmt.Errorf("failed to construct manifest service: %v", err)
				}

				manifestEnumerator, ok := manifestService.(distribution.ManifestEnumerator)
				if !ok {
					return fmt.Errorf("unable to convert ManifestService into ManifestEnumerator")
				}

				var statter distribution.BlobDescriptorService = &linkedBlobStatter{
					blobStore:   registry.blobStore,
					repository:  repo,
					linkPathFns: []linkPathFunc{blobLinkPath},
				}

				manifestEnumerator.Enumerate(ctx, func(dgst digest.Digest) error {
					manifest, err := manifestService.Get(ctx, dgst)
					if err != nil {
						return fmt.Errorf("failed to retrieve manifest for digest %v: %v", dgst, err)
					}

					descriptors := manifest.References()
					for _, desc := range descriptors {
						repobdcp.layers = append(repobdcp.layers, desc.Digest.String())
						_, seen := descriptorSeen[desc.Digest.String()]
						if seen {
							continue
						}

						rDesc, err := statter.Stat(ctx, desc.Digest)
						if err != nil {
							continue
						}

						descriptorSeen[desc.Digest.String()] = rDesc
					}
					return nil
				})

				repoBDCPs = append(repoBDCPs, repobdcp)
				//registry.blobDescriptorServiceMap[repoName] = bdcp
				return nil
			})

			ctx := context.Background()
			for _, repobdcp := range repoBDCPs {
				for _, l := range repobdcp.layers {
					desc, ok := descriptorSeen[l]
					if !ok {
						continue
					}
                    // 刷新cache。
					err := repobdcp.bdcp.SetDescriptor(ctx, desc.Digest, desc)
					if err != nil {
						logrus.Warnf("failed to set descriptor %s, err: %v", desc.Digest, err)
					}
				}
                
				registry.blobDescriptorServiceMap[repobdcp.repoName] = repobdcp.bdcp
			}
		}

		return nil
	}
}
```


以上是sealer对repository的主要做的更改，如果想更细致的了解何时进行缓存。可以看看blobHandler/manifestHandler的GetBlob，GetManifest方法，这些方法sealer没有动。就像上面说的，其实主要返回正确的repository，以及用到正确的remoteURL及authChallenger就能利用好原来的缓存能力进行多镜像仓库缓存了。sealer做的改动已经尽可能小了。