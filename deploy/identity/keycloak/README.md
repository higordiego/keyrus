# Cashflow realm

`realm-cashflow.json` é um template de importação do Keycloak sem credenciais (credential-free). Ele define:

- a aplicação merchant pública usando Authorization Code com PKCE;
- audiences públicas e internas separadas;
- identidades de serviço com privilégio mínimo (least-privilege) para consolidação e reconciliação;
- scopes exatos para merchant, ledger, consolidação e operações;
- dois fixtures locais de merchant cujo `merchant_id` é emitido por um mapper.

Renderize-o fora do repositório fornecendo os quatro secrets necessários através
do ambiente ou Docker secrets em `/run/secrets`:

```sh
deploy/identity/keycloak/render-realm.sh /secure/runtime/realm-cashflow.json
```

O renderizador recusa credenciais vazias ou não resolvidas e cria o resultado
com o modo `0600`. O arquivo gerado contém credenciais e nunca deve ser
comitado ou registrado (logged).

A configuração de runtime em produção pertence ao ticket de topologia de deployment. Ele
deve executar o Keycloak no modo otimizado (optimized mode) com um esquema de identidade
PostgreSQL externo, pelo menos duas réplicas, `jdbc-ping`, rollout serial, configurações de
proxy cientes de TLS (TLS-aware) e health/metrics restritos à rede de gerenciamento. Apenas os
caminhos OIDC enumerados na configuração do KrakenD são públicos.

`go test ./test/integration -run TestKeycloakRealmWithRealIssuer -count=1 -v`
importa o realm renderizado para a imagem fixada (pinned) do Keycloak usando Testcontainers
e verifica tokens client-credential reais através do verificador JWKS de produção.
