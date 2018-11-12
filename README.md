# kube-informer

# build/test
```
CGO_ENABLED=0 GOOS=linux go build -o bin/kube-informer -ldflags '-s -w' cmd/*.go
bin/kube-informer -h
bin/kube-informer --watch=apiVersion:v1,kind:Pod -- env
bin/kube-informer --watch=apiVersion:v1,kind:Pod --pass-args -- echo
bin/kube-informer --watch=apiVersion:v1,kind:Pod --selector='example=true' --pass-stdin -- jq .
bin/kube-informer --watch=apiVersion:v1,kind:ConfigMap --watch=apiVersion:v1,kind:Secret -- env
bin/kube-informer --watch=apiVersion:v1,kind:Pod --leader-elect=endpoints/kube-informer -- env

```

# docker image
```
docker run --rm xiaopal/kube-informer --watch apiVersion:v1,kind:Pod -- env
```