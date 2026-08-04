# ADR-007: Protobuf e gRPC para Comunicação Interna

## O Problema
Serviços em Go se comunicando via REST e JSON em alta velocidade perdem muita performance parseando textos (serialização/deserialização JSON). Além disso, os tipos dos dados em REST são soltos e baseados em documentação que frequentemente ficam desatualizadas.

## A Decisão
Toda comunicação *entre os serviços internos* ocorre primariamente via **gRPC** usando **Protocol Buffers (Protobuf)**.
O OpenAPI público é gerado estaticamente a partir dos próprios arquivos `.proto` (via plugin grpc-gateway).

## Consequências Positivas
* O payload (peso da mensagem de rede) diminui drasticamente, pois Protobuf é binário.
* Existe apenas uma fonte da verdade: O arquivo `.proto`. Não há o risco de a documentação OpenAPI dizer uma coisa e o código implementar outra.
* Performance substancialmente maior nas conexões internas usando HTTP/2 Multiplexado.

## Consequências Negativas
* Curva de aprendizado. Nem todos os desenvolvedores lidaram com gRPC, obrigando a equipe a aprender a usar ferramentas novas de linha de comando (`buf`).
* É mais difícil inspecionar pacotes de rede (usando ferramentas como tcpdump ou Wireshark) porque eles trafegam em formato binário comprimido e encriptado.

## Alternativas Consideradas
* **HTTP/JSON normal e testes de contrato (Pact):** Descartado, pois os testes de contrato geram alto custo de manutenção e continuam mantendo a lentidão do parse JSON interno.

## Gatilhos de Revisão
* Se ferramentas de proxy L7 tiverem grande dificuldade de analisar tráfego binário para fins de segurança, poderíamos adicionar terminadores HTTP, mas é pouco provável dada a maturidade atual do ecossistema Envoy/KrakenD.
