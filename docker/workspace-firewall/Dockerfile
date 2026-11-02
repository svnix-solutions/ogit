# Installs the firewall rules of workspace agents, then exits. Published by
# .github/workflows/workspace-images.yaml.
FROM alpine:3.22
RUN apk add --no-cache iptables
COPY apply.sh /apply.sh
ENTRYPOINT ["/bin/sh", "/apply.sh"]
