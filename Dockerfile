FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM

WORKDIR /

COPY ${TARGETPLATFORM}/stashy /stashy
COPY public/ /public/

ENTRYPOINT ["/stashy"]

CMD ["serve"]
