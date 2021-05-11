package registry

const cacheConfig = `
version: 0.1
log:
  level: debug
  fields:
    service: registry
    environment: development
storage:
  cache:
    blobdescriptor: inmemory
  filesystem:
    rootdirectory: /var/lib/registry
http:
    addr: :5000
    secret: asecretforlocaldevelopment
    debug:
        addr: localhost:5001
    headers:
        X-Content-Type-Options: [nosniff]
proxy:
  remoteurl: https://registry-1.docker.io
  username: username
  password: password
health:
  storagedriver:
    enabled: true
    interval: 10s
    threshold: 3
`
