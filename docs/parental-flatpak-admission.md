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
`SO_PASSCRED` recusa um remetente com PID/UID/GID diferente após transferência
do socket. Uma transição de domínio no mesmo PID ainda requer verificação
antes de autorizar lançamento. O servidor futuro deve fechar a conexão após uma
requisição e não inferir permissão de um peer observado sem validação do
registro parental e da geração da política.

Testes host com Go 1.22.12 e provas de socket real em VM SELinux Enforcing:
[evidência e verificador](evidence/parental-flatpak-admission/CURRENT.md).
A segunda prova observou duas contas em domínios supervisionados distintos e
recusou UID forjado em ambas. Um vínculo isolado com snapshot do catálogo
exige identidade, domínio, geração e política aplicada. Uma terceira prova em
Enforcing recusou escrita por processo filho que herdou o socket, usando
`SO_PASSCRED` para verificar o remetente de cada trecho. Ainda usa fixture
`runcon`; login real, mudança de contexto no mesmo PID, lease de revogação e
enforcement de app continuam gates abertos da issue #102.
