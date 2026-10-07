# Protótipo de admissão para lançamento Flatpak

`internal/parental/flatpak_admission_linux.go` é uma etapa isolada da fronteira
de execução parental. O cliente pode pedir somente um `app_id` ASCII canônico
num frame JSON de até 512 bytes. O serviço lê PID/UID/GID do kernel com
`SO_PEERCRED` e contexto SELinux com `SO_PEERSEC`; não aceita identidade,
argumentos, caminhos nem FDs declarados na mensagem. O rótulo `kernel`, vazio
ou indisponível é recusado.

Isto **não é um broker de produto**. O código não consulta política, não
compara o contexto ao domínio registrado da conta, não resolve ref/origem/
deployment e não inicia Flatpak. `SO_PEERCRED`/`SO_PEERSEC` retratam a conexão;
um socket transferido a outro processo precisa de tratamento explícito antes
de autorizar lançamento. O servidor futuro deve fechar a conexão após uma
requisição e não inferir permissão de um peer observado sem validação do
registro parental e da geração da política.

Testes host com Go 1.22.12 e provas de socket real em VM SELinux Enforcing:
[evidência e verificador](evidence/parental-flatpak-admission/CURRENT.md).
A segunda prova observou duas contas em domínios supervisionados distintos e
recusou UID forjado em ambas. Ainda usa fixture `runcon`; vínculo com o catálogo,
transferência de socket e enforcement de app continuam gates abertos da issue #102.
