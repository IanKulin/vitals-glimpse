FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /vitals-glimpse main.go

FROM alpine:3.19
COPY --from=build /vitals-glimpse /usr/local/bin/vitals-glimpse
EXPOSE 10321
ENTRYPOINT ["vitals-glimpse"]
