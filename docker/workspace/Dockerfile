# The image of workspaces: code-server, pinned by digest, with the usual build
# tools. Published by .github/workflows/workspace-images.yaml.
FROM codercom/code-server@sha256:b9e8056bb74bd0c4f90284c7559eddeccd38400cfedc28e64c9d3ca50fef1f83

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential nodejs npm python3 python3-pip python3-venv \
 && rm -rf /var/lib/apt/lists/*
COPY product.py /tmp/product.py
RUN python3 /tmp/product.py /usr/lib/code-server/lib/vscode/product.json && rm /tmp/product.py
USER 1000:1000
