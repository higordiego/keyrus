# ADR-010: Omissão de Dados Sensíveis (Redaction) em Logs

## O Problema
Durante debug e operações em produção, é praxe da engenharia "logar tudo que acontece". Porém, em sistemas financeiros, logar payloads e *headers* completos significa salvar Tokens JWTs, valores monetários exatos, chaves públicas e descrições de compra em bancos de dados de monitoramento acessíveis a toda a equipe de engenharia. Isso viola normas de segurança rigorosas, PCI-DSS e a privacidade do usuário (LGPD).

## A Decisão
Os ambientes de *Logging* (usando a nova biblioteca `log/slog` do Go) utilizarão middlewares que fazem filtragem profunda (*Redaction*).

* Headers como `Authorization` não são registrados no tráfego HTTP.
* `Idempotency-Key` é ofuscada.
* O valor de créditos/débitos (`amount`) e strings textuais (descrições) nunca são despejados nos Logs estruturados (JSON). O único dado passível de visualização são os *IDs* (como `entry_id` ou `merchant_id`) e códigos de status.

## Consequências Positivas
* Evitamos incidentes de PII (Personally Identifiable Information) e mantemos a integridade operacional da infraestrutura intocável por invasores (não podem ler chaves nos logs).
* O ambiente de auditoria e desenvolvimento não precisa ter controle de acesso tão severo quanto a produção.

## Consequências Negativas
* Engenheiros que cuidam de suporte (SRE/N3) precisarão depender muito mais de IDs e *traces* e acessar os dados nos bancos usando ferramentas controladas (via queries seguras) em vez de apenas ler as cargas úteis nos logs no Grafana/Kibana.

## Gatilhos de Revisão
* Se essa regra impedir drasticamente a investigação de problemas comuns, precisaremos investir em um cofre de mascaramento dinâmico externo ao invés da filtragem no middleware interno, o que não compensa o risco e a complexidade na fase atual.
