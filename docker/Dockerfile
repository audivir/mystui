FROM alpine:3.22 AS microsocks
RUN apk add --no-cache gcc musl-dev make
ADD https://github.com/rofl0r/microsocks/archive/refs/heads/master.tar.gz /src.tar.gz
RUN mkdir /src && tar -xzf /src.tar.gz -C /src --strip-components=1 && make -C /src

FROM alpine:3.22
ARG MYSTVPN_VERSION=0.1.2

# runtime dependencies listed in the mystvpn README
RUN apk add --no-cache bash curl ca-certificates tar gzip procps \
    wireguard-tools iproute2 iptables nftables openresolv

RUN set -e; \
    case "$(uname -m)" in x86_64) a=amd64;; aarch64) a=arm64;; armv7l|armv8l) a=arm32;; *) echo "unsupported $(uname -m)"; exit 1;; esac; \
    base=https://github.com/mysteriumnetwork/mysteriumvpncli/releases/download/$MYSTVPN_VERSION; \
    mkdir /tmp/dl && cd /tmp/dl; \
    curl -fsSLO "$base/mystvpn-linux-$a.tar.gz"; \
    curl -fsSLO "$base/mystvpn-linux-$a.tar.gz.sha256"; \
    sha256sum -c "mystvpn-linux-$a.tar.gz.sha256"; \
    mkdir x && tar -xzf "mystvpn-linux-$a.tar.gz" -C x; \
    install -m755 "$(find x -name mystvpn -type f | head -1)" /usr/local/bin/mystvpn; \
    rm -rf /tmp/dl

# small SOCKS5 server, not packaged for Alpine
COPY --from=microsocks /src/microsocks /usr/local/bin/microsocks

# tokens and WireGuard state live under $HOME/.config.
ENV HOME=/data
VOLUME /data
EXPOSE 1080

# the tunnel comes up with `docker exec mystvpn mystvpn connect ...`.
# the proxy runs in the same network namespace, so its traffic leaves through the tunnel.
CMD ["microsocks", "-i", "0.0.0.0", "-p", "1080"]
