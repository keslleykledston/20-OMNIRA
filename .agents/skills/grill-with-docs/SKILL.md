# Skill: grill-with-docs

## Quando usar
No começo de uma feature, mudança arquitetural ou requisito ainda ambíguo.

## Procedimento
1. Leia `CONTEXT.md`, spec relevante, ADRs e código relacionado.
2. Separe fatos já respondidos pelo repositório de decisões humanas.
3. Trabalhe **uma decisão por vez**.
4. Para cada decisão, apresente opções e uma recomendação com trade-offs.
5. Quando um termo de domínio ficar estável, atualize `CONTEXT.md`.
6. Quando uma decisão difícil de reverter ficar estável, proponha/atualize um ADR.
7. Mantenha `docs/decisions/OPEN-QUESTIONS.md` com o que sobrou.
8. Pare quando não houver branch importante silenciosamente assumida.

## Não fazer
- questionário massivo;
- ADR para escolha trivial;
- perguntar algo que o código/docs já respondem;
- misturar glossário com detalhe de implementação.
