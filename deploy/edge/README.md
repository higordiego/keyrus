# Public edge

`krakend/krakend.json` é a superfície pública allow-listed (em lista de permissões) para o KrakenD Community
Edition 2.10. Ele publica cinco rotas de negócios e a superfície mínima do protocolo
OIDC. Ele não publica endpoints de administração do Keycloak, health, metrics, o
master realm, endpoints de gerenciamento de aplicação, ou métodos gRPC internos.

Rotas protegidas fixam (pin) RS256, issuer, audience e scopes exatos. Os headers são
allow-listed: `Authorization`, W3C trace context e, em rotas de comando,
`Idempotency-Key` alcançam o adapter; headers forjáveis (spoofable) de tenant/proxy e
`baggage` não.

O `tracestate` público não é confiável. Os adapters preservam apenas o marcador
fixo livre de dados `cashflow=public-edge`; todos os outros valores são descartados. O
Collector também limpa o `trace_state` do span antes do export, para que a instrumentação
anterior do KrakenD não possa exportar um valor opaco do cliente. O `traceparent` permanece
sendo a fonte de correlação através de HTTP e gRPC.

Nenhum componente de retry (tentativa) está configurado em qualquer `POST` de negócios. Uma resposta com falha
é retornada ao cliente, cuja repetição explícita deve reutilizar a mesma
`Idempotency-Key`. Os rate limits (limites de taxa) do roteador são locais para cada réplica do KrakenD:
eles são proteção contra abusos, não uma cota distribuída ou um controle financeiro.

As três rotas OIDC de browser/token têm um budget de backend de 15 segundos. O
`write_timeout` global é de 20 segundos para que um Keycloak frio (cold) ainda tenha cinco segundos de
margem de response-write; ele deve permanecer sempre maior que o maior timeout
de endpoint. Erros de transporte são terminais no PKCE smoke e nunca sofrem retry.

A imagem final declara um Docker `HEALTHCHECK` contra o probe de loopback
`GET /__health` integrado do KrakenD. O E2E aguarda pelo Docker state `healthy`, assevera
o UID/GID 65532 e comprova separadamente que os caminhos de gerenciamento da aplicação/Keycloak,
health e metrics estão ausentes da lista de permissão de rotas públicas.

Valide o arquivo com a mesma versão fixada (pinned) do Community Edition:

```sh
docker run --rm \
  -v "$PWD/deploy/edge/krakend/krakend.json:/etc/krakend/krakend.json:ro" \
  krakend:2.10 check -c /etc/krakend/krakend.json --lint
```

O Go validator em `internal/platform/edge/krakendconfig` adiciona invariantes
específicos do projeto que o schema do upstream não consegue expressar.
