# Política de segurança (Security policy)

## Reportando uma vulnerabilidade (Reporting a vulnerability)

Não abra uma issue pública com detalhes de exploit, credentials, dados de clientes ou dados financeiros. Use os relatórios de vulnerabilidade privados do GitHub quando eles estiverem ativados para este repositório. Até lá, entre em contato com o proprietário do repositório por meio de um canal privado e inclua o commit afetado, impacto, passos para reprodução e uma prova de conceito (proof of concept) segura.

O maintainer confirmará o recebimento de um relatório em até dois dias úteis, fará a triagem de severidade e alcance (reachability), e coordenará a remediação e divulgação. Não há nenhuma promessa de bug-bounty.

## Versões suportadas (Supported versions)

Apenas o commit mais recente na `main` tem suporte durante o architecture challenge. Secrets nunca são aceitas em reports, fixtures, logs ou commits.

## Gates de segurança (Security gates)

`make security` varre (scans) vulnerabilidades Go alcançáveis (reachable), o histórico do repositório, o working tree, dependências, e a configuração. `make policy` impõe referências imutáveis de Action e a política de workflow de privilégio mínimo (least-privilege). Vulnerabilidades HIGH/CRITICAL alcançáveis, secrets confirmadas, e configuração insegura crítica bloqueiam alterações.

Qualquer exceção temporária de gate exige owner nomeado, justificativa, mitigação, validade e critério de remoção, registrada em uma issue ou registro de PR; descobertas de secret não podem ser perdoadas (waived).
