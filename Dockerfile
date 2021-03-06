FROM scratch

HEALTHCHECK --retries=10 CMD [ "/viws", "-url", "https://localhost:1080/health" ]

EXPOSE 1080
ENTRYPOINT [ "/viws" ]

COPY cacert.pem /etc/ssl/certs/ca-certificates.crt
COPY bin/viws /viws
