FROM busybox
MAINTAINER Jo Vandeginste <jo.vandeginste@gmail.com>
ADD ./bin/go-docker-graphite.lnx64 /bin/go-docker-graphite
CMD /bin/go-docker-graphite /config.yaml
