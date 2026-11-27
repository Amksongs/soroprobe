# Recorded Test Fixtures

Test fixtures in this directory are recorded from the Stellar testnet and replayed by `stellartest.Fake` during unit and integration tests. Tests never connect to a live network.

## Contracts and Fixtures

- **Stellar Asset Contract (SAC)**: `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC` (Native XLM SAC)
  - `ledger_entries_sac_instance.json`: Instance entry for the native SAC contract.
  - `ledger_entries_data_durability.json`: Contract data entries recorded from the native SAC (`CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC`), covering both storage durabilities:
    - **Persistent**: `vec:["sym:Balance","addr:CBXZBQ6NW3TGPGLTR4JCG2VPVWFLCCHKDW75NJIXYXV7CJLNUO64VTFC"]` (balance entry with persistent durability).
    - **Temporary**: `vec:["sym:Allowance","map:[[\"sym:from\",\"addr:GB32JP5GNOQLW4NUMV6H2UBOSMB4DROL7YNDBWAHYXMVFLLJOH3LXKHK\"],[\"sym:spender\",\"addr:CA6P26K4QNNIMTYP22ILTSCXQQDEKNWJIZJPC34YEZYOLBNT7YUPX7XS\"]]"]` (allowance entry with temporary durability).

- **Wasm Contract**: `CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4`
  - `ledger_entries_wasm_instance.json`: Wasm contract instance entry.
  - `ledger_entries_wasm_code.json`: Wasm contract bytecode entry.

- **Undeployed Contract**: `CCV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XMCW`
  - `ledger_entries_undeployed.json`: Empty response for an uninstantiated contract address.

- **Simulations**:
  - `simulate_success.json`: Successful simulation of the `decimals` call on SAC.
  - `simulate_failure.json`: Failed simulation of a non-existent function (`no_such_fn`).
