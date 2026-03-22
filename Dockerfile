FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM

COPY ${TARGETPLATFORM}/stashy /stashy

ENTRYPOINT ["/stashy"]
