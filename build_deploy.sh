#!/bin/bash

CGO_ENABLED=0 go build ./cmd/concertmaster
docker build -t quay.io/apahim/concertmaster:v13 .
docker push quay.io/apahim/concertmaster:v13
