FROM ai-gateway-harness:20261027-harness-upload-controls-r1
USER root
ENV NARB_DISABLE_NATIVE_CACHE=1
RUN apk add --no-cache iptables
COPY integrations/harness-users /opt/harness-users
RUN sed -i 's/\r$//' /opt/harness-users/start.sh
COPY integrations/harness/entry.mjs /opt/harness/users-entry.mjs
COPY integrations/harness/ui/workspace-files.mjs /opt/harness/node_modules/@ai-gateway/harness-ui/workspace-files.mjs
COPY integrations/harness/ui/index.mjs /opt/harness/node_modules/@ai-gateway/harness-ui/index.mjs
COPY integrations/harness/ui/client.template.js /opt/harness/node_modules/@ai-gateway/harness-ui/client.template.js
RUN node /opt/harness/node_modules/@ai-gateway/harness-ui/build.mjs
WORKDIR /opt/harness-users
ENTRYPOINT ["/bin/sh", "/opt/harness-users/start.sh"]
