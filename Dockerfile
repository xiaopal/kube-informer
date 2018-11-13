FROM golang:1.10 as build
ADD . /go/src/github.com/xiaopal/kube-informer
WORKDIR  /go/src/github.com/xiaopal/kube-informer
RUN CGO_ENABLED=0 GOOS=linux go build -o /kube-informer -ldflags '-s -w' cmd/*.go
RUN chmod +x /kube-informer

FROM alpine:3.7

RUN apk add --no-cache bash coreutils curl openssh-client openssl git findutils && \
	curl -sSL 'http://npc.nos-eastchina1.126.net/dl/jq_1.5_linux_amd64.tar.gz' | tar -zx -C /usr/bin && \
	curl -sSL "http://npc.nos-eastchina1.126.net/dl/dumb-init_1.2.0_amd64.tar.gz" | tar -zx -C /usr/bin && \
	curl -sSL 'https://npc.nos-eastchina1.126.net/dl/kubernetes-client-v1.9.3-linux-amd64.tar.gz' | tar -zx -C /usr/ && \
	ln -s /usr/kubernetes/client/bin/kubectl /usr/bin/kubectl
	
COPY --from=build /kube-informer /kube-informer
RUN ln -s /kube-informer /usr/bin/kube-informer

ENTRYPOINT [ "/kube-informer" ]