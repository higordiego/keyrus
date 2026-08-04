# ADR-004: Keycloak e JWT no Gateway

## O Problema
No legado, o serviço financeiro tentava cuidar de autenticação, o que abria brechas de segurança, vazava lógica de negócio para a borda e dificultava a integração com serviços de terceiros. Se houvesse uma mudança na regra de senhas, o Ledger precisaria ser recompilado e redistribuído.

## A Decisão
Delegamos 100% da identidade para o **Keycloak** atuando como Provedor de Identidade (IdP) OIDC, e exigimos que a verificação do JWT ocorra na porta de entrada da rede (**KrakenD API Gateway**).

Nenhum serviço interno da nossa arquitetura gera ou verifica assinaturas de senha. O Gateway recebe o JWT, valida a assinatura e os escopos, extrai o ID do comerciante (`merchant_id`) de dentro do token e encaminha para a Ledger API através de cabeçalhos seguros que o usuário final não pode forjar.

## Consequências Positivas
* O código financeiro não sabe o que é uma senha. A superfície de ataque diminui consideravelmente.
* Podemos adicionar Autenticação de Dois Fatores (2FA) amanhã no Keycloak sem alterar uma linha sequer no código do Ledger ou do Consolidado.

## Consequências Negativas
* Complexidade operacional: Precisamos sustentar e monitorar um cluster de Keycloak no nosso ambiente (banco de dados, configuração de cache, etc).
* O KrakenD agora tem forte acoplamento às chaves públicas (JWKS) do Keycloak. Se a comunicação entre eles cair, ninguém consegue fazer login.

## Gatilhos de Revisão
* Se o custo de manutenção do Keycloak se mostrar excessivamente pesado para a equipe de infraestrutura, podemos substituí-lo por um IdP gerenciado (como AWS Cognito ou Auth0).
