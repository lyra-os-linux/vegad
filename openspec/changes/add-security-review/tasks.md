# Tarefas (vegad)

Tarefas 3.1 a 3.6 da proposta-mãe.

- [x] 3.1 `network.go`: parse do `ss`, alcance, unidade pelo cgroup (só a folha) e cruzamento com o firewalld, com testes sobre as capturas reais
- [x] 3.2 `ListListening` como leitura intermediada silenciosa (`firewall.read-admin`), com o teste de roteamento das leituras em 11
- [x] 3.3 Teste `security` com fixtures reais e sem nenhum hash no relatório
- [x] 3.4 Remédios com validação estrita e o teste de argumento adulterado
- [x] 3.5 Perfis: no `server`, SSH e 9090 são esperados
- [x] 3.6 Integração contra o contrato. Execução real pelo D-Bus na workstation: nada exposto; com uma porta 8000 liberada de propósito, o Resolver `close-port` fechou e o reteste ficou verde
