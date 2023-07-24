#!/bin/bash

CGO_ENABLED=0 go build ./cmd/concertmaster
docker build -t quay.io/apahim/concertmaster:v2 .
docker push quay.io/apahim/concertmaster:v2
