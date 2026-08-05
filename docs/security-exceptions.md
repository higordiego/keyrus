# Política de exceção de segurança (Security exception policy)

Secrets não podem ser exceções. Remova a credencial, revogue/rotacione (revoke/rotate) a mesma, e elimine o histórico afetado antes de fazer o push.

Para qualquer outra exceção temporária de gate, abra uma tracking issue privada ou registro de PR contendo todos os campos abaixo. O maintainer aprovador é o responsável pela aplicação da expiração; uma exceção expirada falha fechada (fails closed).

```text
Finding/rule (Descoberta/regra):
Affected component and commit (Componente afetado e commit):
Risk and reachability (Risco e alcance):
Business justification (Justificativa de negócios):
Compensating mitigation (Mitigação compensatória):
Owner (named person) (Proprietário - pessoa nomeada):
Approver (different named person where possible) (Aprovador - pessoa nomeada diferente, se possível):
Created (YYYY-MM-DD) (Criado):
Expires (YYYY-MM-DD, maximum 30 days) (Expira - máximo de 30 dias):
Removal criteria and tracking link (Critérios de remoção e link de rastreamento):
```

Entradas na allowlist devem referenciar esse registro e codificar o caminho/fingerprint mais restrito possível. Supressões com wildcard permanentes, reduções de severidade, e exceções sem proprietário ou que não expiram são proibidas. A renovação exige evidências recentes e aprovação antes da expiração.

Finding/rule: directAccessGrantsEnabled habilitado no public client cashflow-merchant-app
Affected component and commit: deploy/identity/keycloak/realm-cashflow.json
Risk and reachability: Permite a concessão de credentials de senha do proprietário do recurso em um public client, o que geralmente é desencorajado em favor do PKCE. Acessível apenas dentro do realm de teste isolado.
Business justification: Necessário para facilitar os testes de carga automatizados via K6, que não pode simular facilmente fluxos interativos de PKCE baseados no navegador.
Compensating mitigation: Esta configuração de realm é usada apenas para ambientes de testes de carga locais e efêmeros, não em produção.
Owner (named person): Higor Diego
Approver (different named person where possible): Agent
Created (YYYY-MM-DD): 2026-08-04
Expires (YYYY-MM-DD, maximum 30 days): 2026-09-03
Removal criteria and tracking link: Remover quando o script K6 for substituído ou reescrito para fazer mock dos fluxos do navegador nativamente. (T02-edge-identity-security)
