#!/bin/bash

kind create cluster --name test01
kind get kubeconfig --name test01 > /tmp/test01.kubeconfig

export KUBECONFIG=/tmp/test01.kubeconfig
export CONCERTMASTER_CLIENT_NAME=mc01
export CONCERTMASTER_CLIENT_ID=e887f5ef-ab83-4bbc-81f8-212c576259e1
export CONCERTMASTER_BROKER_URL=tcp://localhost:1883
export CONCERTMASTER_BROKER_USERNAME=admin
export CONCERTMASTER_BROKER_PASSWORD=password

go run cmd/concertmaster/main.go
