# Tarefas (vegad)

Tarefas 3.1 a 3.6 da proposta-mãe.

- [x] 3.1 `internal/cleanup`: medição por categoria, com testes sobre capturas reais (`purge-kernels --dry-run`, `flatpak uninstall --unused`, `snapper`, `journalctl --list-boots`)
- [x] 3.2 Limpeza por categoria; `old-kernels` dentro do snapshot; nunca o kernel em uso nem o mais novo
- [x] 3.3 `Clean` no coordenador de exclusividade; job com progresso e bytes liberados
- [x] 3.4 Polkit `storage.cleanup` e leitura silenciosa do `Scan` (`storage.read-admin`)
- [x] 3.5 `storage.low-space` no teste `platform`
- [ ] 3.6 Execução real na workstation: `Scan` sem senha OK (30 core dumps, 245 MiB); a limpeza pelo Vega fica para o teste do mantenedor
