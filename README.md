# kube-informer

# build/test
```
CGO_ENABLED=0 GOOS=linux go build -o bin/kube-informer -ldflags '-s -w' cmd/*.go
bin/kube-informer -h
bin/kube-informer --watch=apiVersion=v1,kind=Pod -- env

bin/kube-informer --watch=apiVersion=v1,kind=Pod --pass-args -- echo
bin/kube-informer --watch=apiVersion=v1,kind=Pod --selector='example=true' --pass-stdin -- jq .
bin/kube-informer --watch=apiVersion=v1,kind=Pod --selector='example=true' --field-selector='status.phase=Running' --pass-stdin -- jq .

bin/kube-informer --watch=apiVersion=v1,kind=ConfigMap --watch=apiVersion=v1,kind=Secret -- env
bin/kube-informer --watch=apiVersion=v1,kind=ConfigMap:apiVersion=v1,kind=Secret -- env

bin/kube-informer --watch=apiVersion=v1,kind=Pod --leader-elect=endpoints/kube-informer -- env

docker run -it --rm -v /root:/root -v $PWD/bin/kube-informer:/usr/bin/kube-informer debian:8 \
kube-informer --watch apiVersion=v1,kind=ConfigMap --leader-elect=configmaps/kube-informer -- \
bash -c 'sleep 1.5s & sleep 1s && echo $INFORMER_EVENT $INFORMER_OBJECT_NAMESPACE.$INFORMER_OBJECT_NAME'

```

# docker image
```
docker run -it --rm -v /root:/root xiaopal/kube-informer --watch apiVersion=v1,kind=Pod -- bash -c 'echo $INFORMER_EVENT $INFORMER_OBJECT_NAMESPACE.$INFORMER_OBJECT_NAME'
```

# index server
```
bin/kube-informer --watch apiVersion=v1,kind=Pod --watch apiVersion=v1,kind=ConfigMap --index-server=:8080 --index namespace='{{.metadata.namespace}}'

curl 'http://127.0.0.1:8080/index?key=default/busybox'
curl 'http://127.0.0.1:8080/index?keys&offset=0&limit=200'
curl 'http://127.0.0.1:8080/index?list&offset=0&limit=200'

curl 'http://127.0.0.1:8080/index/'
curl 'http://127.0.0.1:8080/index/namespace?keys&offset=0&limit=200'
curl 'http://127.0.0.1:8080/index/namespace?key=default&offset=0&limit=200'

curl 'http://127.0.0.1:8080/index?list&watch=1&offset=0&limit=200'

```

# webhook
```
bin/kube-informer --watch=apiVersion=v1,kind=Pod --webhook http://127.0.0.1:8888/webhook 

bin/kube-informer --watch=apiVersion=v1,kind=Pod --webhook http://127.0.0.1:8888/webhook  --webhook-param name='{{.metadata.name}}' 

```