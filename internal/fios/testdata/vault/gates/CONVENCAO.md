---
titulo: Convencao de gates (fixture)
---

# Convencao de gates

Este arquivo NAO e um ledger. Ele existe na fixture com uma caixa de verdade
ancorada em `^` justamente para que o teste falhe se o leitor parar de
descarta-lo por nome:

- [ ] EXEMPLO: caixa de documentacao que nunca pode virar card
  CHECK: `grep -c '^- \[ \]' gates/CONVENCAO.md`
  EXPECT: 1, e zero cards no board
