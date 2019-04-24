Redis Exporter Mutating Admission Webhook
=========================================

> Remaw [re-maw] - Adding Redis exporter sidecar to your Redis-application

## Getting started

Add the following to your `nais.yaml`, and [Naiserator]() will fix the rest:
```
metadata:
  annotations:
    redis-exporter: true
```
