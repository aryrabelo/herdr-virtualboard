# ledger com caixa mal formada

## Gates

- [~] B1: bloqueado sem dono declarado
  CHECK: `ls /nada`
  EXPECT: exit 2

- [ ] B2: caixa acionavel bem formada, tem que sobreviver ao vizinho quebrado
  CHECK: `echo ok`
  EXPECT: ok

- [x] B3: fechada sem evidencia
  CHECK: `echo ok`
  EXPECT: ok

- [?] B4: marcador que a convencao nao define
  CHECK: `echo ok`
  EXPECT: ok
