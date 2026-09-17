---
titulo: Ledger da rodada factory-fix (fixture)
---

# factory-fix

## Gates

- [ ] G0: caixa nova enfiada ANTES da G1
  CHECK: `echo novo`
  EXPECT: novo

- [x] G1: binario instalado na frota
  CHECK: `which hvb`
  EXPECT: um caminho, exit 0
  EVIDENCE: /opt/homebrew/bin/hvb (exit 0, medido 2026-09-16)

- [ ] G2: a fila le o vault do dono
  CHECK: `hvb queue --json | jq 'length'`
  EXPECT: 7 acionaveis

- [~] G3: credencial readonly no cofre
  CHECK: `op item get gh-token --fields token`
  EXPECT: exit 0
  BLOQUEIO: Ary · mintar o token readonly no 1Password e gravar em `op://frota/gh-token`

- [-] G4: painel proprio em Grafana
  SUPERADO: o board do hvb cobre a mesma leitura, medido neste ledger

## Rodape

Nada abaixo deste heading pertence a caixa G4.
