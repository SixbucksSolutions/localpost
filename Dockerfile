FROM scratch

# Build with the verified, unpacked release archive as the build context.
ARG VERSION=0.1.0
ARG SOURCE_REVISION=852b8a9ded0d03e98cada5810b18c92746848545
ARG PACKAGING_REVISION

LABEL org.opencontainers.image.title="localpost" \
      org.opencontainers.image.description="A localhost HTTP-to-file delivery service" \
      org.opencontainers.image.source="https://github.com/SixbucksSolutions/localpost" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      io.localpost.packaging-revision="${PACKAGING_REVISION}"

COPY --chmod=0555 localpost /localpost
COPY LICENSE build-info.json openapi.json /usr/share/localpost/
COPY licenses/ /usr/share/localpost/licenses/

USER 65532:65532
EXPOSE 53147/tcp
STOPSIGNAL SIGINT
ENTRYPOINT ["/localpost"]
