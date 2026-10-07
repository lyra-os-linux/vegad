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

`ResolveFlatpakDeployment` é um seletor puro para um inventário **fornecido
por fonte confiável**: recusa duas instalações do mesmo app, escopo/UID de
usuário incompatível, mudança de origem ou publicador, ref incompleto, commit
abreviado e runtime de arquitetura diferente. Uma assinatura/publicador
verificado pode manter aprovação após atualização; sem identidade verificável,
a aprovação fica presa ao commit exato. O nome do remote não autentica o
publicador. Ainda falta produzir esse inventário com identidade verificada e
fixar app/runtime durante o lançamento; chamadas independentes a `flatpak
info` não formam um snapshot atômico.

O [ensaio com dois apps instalados](evidence/parental-flatpak-inventory/CURRENT.md)
confirmou refs/commits completos e links `active` distintos, mas os remotes
de fixture estão sem verificação GPG. Por isso, o caso sem publicador
verificável continua limitado ao commit exato. O ensaio é diagnóstico e não
fornece a fonte confiável exigida pelo seletor.

O [ensaio de commit e FD](evidence/parental-flatpak-commit-pin/CURRENT.md)
mostrou que `--commit` e `--runtime-commit` aceitam o par correto e recusam
commits trocados ou inexistentes. Também mostrou que `--app-fd` pode fornecer
o conteúdo de B ao pedir A. Logo, um futuro broker deve construir esses
argumentos e FDs exclusivamente a partir da regra/inventário confiáveis, e o
domínio da criança não pode executar `flatpak run` diretamente.

`BuildFlatpakRunArgs` agora produz uma lista fechada de parâmetros a partir do
deployment selecionado. O [teste no Flatpak 1.16.6](evidence/parental-flatpak-run-args/CURRENT.md)
executou A e B com commits corretos e recusou app/runtime trocados. Rodou em
Permissive com dados de fixture, sem broker, fonte autenticada ou política MAC.

`WithFlatpakLease` reutiliza a ordem de locks do registro parental (catálogo,
inventário, gate da conta) e revalida peer, geração, prova local, adaptadores
e marcadores de bloqueio antes de entregar um callback síncrono. Testes com
arquivos reais confirmam que os locks ficam retidos e que mudanças entre o
vínculo inicial e esse callback falham fechadas. O código ainda não chama o
Flatpak, não verifica uma aprovação de app e não cobre escritores que ignorem
esses locks nem uma mudança de contexto SELinux no mesmo PID.
