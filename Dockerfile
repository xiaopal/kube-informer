FROM golang:1.10 as build
ADD . /go/src/github.com/xiaopal/kube-informer
WORKDIR  /go/src/github.com/xiaopal/kube-informer
RUN CGO_ENABLED=0 GOOS=linux go build -o /kube-informer -ldflags '-s -w' cmd/*.go && \
	chmod +x /kube-informer

FROM xiaopal/kube-leaderelect
	
COPY --from=build /kube-informer /kube-informer
RUN ln -s /kube-informer /usr/bin/kube-informer

ENTRYPOINT [ "/kube-informer" ]