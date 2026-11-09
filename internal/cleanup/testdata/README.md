# Fixtures da limpeza

Saídas usadas pelos testes de `internal/cleanup`. Partem de capturas numa
estação Lyra 16.1 (29/10/2026), mas **nem todas são capturas sem edição**
(vegad#127):

| Arquivo | Origem |
| --- | --- |
| `list-boots.json` | `journalctl --list-boots -o json` real; os `boot_id` foram trocados por valores sequenciais. |
| `flatpak-unused.txt` | Editada: o texto diz "user installation", mas o comando testado é `--system`. Vale pela tabela, que é o que o parser lê. |
| `flatpak-unused-nothing.txt` | Saída de "nada a remover" do `flatpak uninstall --unused`. |
| `purge-kernels-remove.txt` | Editada: montada para listar três kernels a remover com o kernel em uso fora da lista. |
| `purge-kernels-nothing.txt` | Saída de "nada a fazer" do `zypper purge-kernels --dry-run`. |
| `snapper-config.csv`, `snapper-list.csv` | `snapper --csvout get-config` e `snapper --csvout list` da estação. |

Ao trocar uma fixture por uma captura nova, atualize esta tabela.
