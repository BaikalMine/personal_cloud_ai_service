ARG BASE_IMAGE=ai-gateway-harness:0.2.1-alpha.2-gateway-ui-r2
FROM ${BASE_IMAGE}
COPY --chown=node:node entry.mjs /opt/harness/entry.mjs
COPY --chown=node:node model-policy.mjs /opt/harness/model-policy.mjs
