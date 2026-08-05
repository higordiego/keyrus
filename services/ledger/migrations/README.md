# Migrations do Ledger

`Apply` é o caminho de produção forward-only (somente avanço). Ele registra um checksum SHA-256 para
cada migration aplicada e se recusa a continuar quando um arquivo já aplicado não
corresponde mais ao seu checksum registrado.

`000001_ledger_core` é a migration imutável publicada em `37596ee`.
`000002_ledger_integrity` adota o seu checksum histórico, atualiza as
referências tenant-aware sem recriar tabelas, e correlaciona cada aggregate ID da outbox
com o position daquela mesma entry. Um banco de dados legado é aceito
apenas quando o seu rastreador (tracker) e as constraints críticas da `000001` correspondem ao estado confiável (trusted state).
`000003_outbox_publisher` adiciona o expiring lease utilizado por publishers concorrentes;
o financial event e o seu `event_id` estável permanecem inalterados.

`RollbackAll` e os arquivos `*.down.sql` são **destrutivos**: eles removeem o
schema `ledger` inteiro, incluindo financial entries, respostas de idempotency e
outbox events. Eles são restritos a bancos de dados de desenvolvimento descartáveis ou a um
procedimento de recuperação (recovery procedure) explicitamente aprovado com um backup verificado. Eles não são um
mecanismo normal de rollback em produção.

Para realizar o rollback apenas do binário da aplicação, deixe o banco de dados em `000002`. O
schema permanece compatível com a aplicação `37596ee` porque a atualização
apenas fortalece as constraints e adiciona metadados de migration. Não execute
`RollbackAll`: um rollback de aplicação nunca requer a exclusão de dados do Ledger.

## Grants mínimos de runtime do Ledger

O runtime da API utiliza uma role non-superuser. Substitua `ledger_runtime` pelo
nome da role implantada:

```sql
GRANT USAGE ON SCHEMA ledger TO ledger_runtime;
GRANT SELECT, INSERT, UPDATE ON ledger.merchant_position TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.ledger_entry TO ledger_runtime;
GRANT UPDATE (id) ON ledger.ledger_entry TO ledger_runtime;
GRANT SELECT, INSERT, UPDATE ON ledger.idempotency_record TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.outbox_event TO ledger_runtime;
```

O privilégio de `UPDATE (id)` em nível de coluna (column-level) é necessário pelo PostgreSQL para
`SELECT ... FOR UPDATE` durante a serialização de reversão. A trigger de linha imutável (immutable-row trigger)
ainda rejeita qualquer `UPDATE` ou `DELETE` real contra `ledger_entry`. Os grants do publisher
são deliberadamente separados:

```sql
GRANT USAGE ON SCHEMA ledger TO outbox_publisher;
GRANT SELECT ON ledger.outbox_event TO outbox_publisher;
GRANT UPDATE (
    available_at, published_at, attempts, last_error, lease_owner, lease_until
) ON ledger.outbox_event TO outbox_publisher;
```
