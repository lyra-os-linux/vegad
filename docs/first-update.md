# Preparação dos repositórios (primeiro boot)

`vegad-first-update.service` roda `vegad first-update` uma única vez no
primeiro boot de um Lyra OS Desktop instalado:

1. confere que `/usr/share/vega/keys/lyra-package-signing-keyring.asc` contém
   exatamente os fingerprints fixados em `internal/distro/trusted_keys.go` e
   importa essas chaves no RPM (`rpmkeys --import`);
2. `zypper --non-interactive refresh` em todos os repositórios;
3. lista as atualizações nativas disponíveis usando os metadados recém-atualizados;
4. publica o estado em `/var/lib/vega/update-status.json` e os sinais D-Bus
   de atualizações disponíveis;
5. grava `/var/lib/vega/first-update.done` após o sucesso dessas etapas.

A instalação de pacotes fica no fluxo normal do Vega. Esta rotina não executa
`zypper update` nem cria snapshots. A publicação do estado não faz outro
refresh nem consulta Flatpak: preserva sua última contagem conhecida, quando
existente. A verificação periódica continua atualizando as duas contagens.

Os nomes `first-update` do comando, da unit e do marcador são preservados por
compatibilidade com as instalações existentes.

O Zypper nunca recebe `--gpg-auto-import-keys`. Uma chave fora da lista fixada
faz o refresh falhar. A proposta persistente pode ser revisada e aprovada explicitamente no painel
do Vega, conforme a seção de revisão de chaves abaixo.

## Quando roda

- Só com `/usr/lib/lyra-os/release` presente (imagem Lyra OS) e perfil
  `desktop`. Instalar o vegad num openSUSE Leap existente não dispara nada.
- Nunca na sessão live (`rd.live.image`/`root=live:` na linha de comando do
  kernel).
- A unit é habilitada no `%post` apenas na primeira instalação do pacote (caso
  da imagem da ISO). Upgrades preservam a preparação pendente ou concluída.
- Ao atualizar uma versão antiga que ainda não incluía essa unit, o `%pre`
  registra a dispensa em `/var/lib/vega/first-update.skipped`. A presença da
  unit anterior é verificada antes da instalação dos novos arquivos. Essa
  dispensa vale também para a execução manual de `vegad first-update`.
- `first-update.done` é gravado pela rotina após o sucesso; o RPM não cria
  esse marcador. Marcadores de versões anteriores são preservados, pois não
  é possível distinguir com segurança sucesso real de dispensa antiga.
- É `Type=exec`: o boot e o login não esperam a preparação terminar.

## Vega durante a preparação

Enquanto a unit está ativa, o vegad reserva as operações nativas para a preparação. O vegad recusa
logo de início instalar, remover ou atualizar pacotes nativos, limpar o cache
e mexer em repositórios, antes de pedir senha ao Polkit ou de criar um
snapshot, com o erro `org.lyraos.Vega1.Error.FirstUpdateInProgress`: "O sistema
está preparando os repositórios. Tente novamente em alguns minutos."
Buscas, detalhes e listas de atualizações que esbarrarem no lock (saída 7 do
Zypper) recebem a mesma mensagem. Operações Flatpak não usam o lock e seguem
normalmente. Se o lock for de outra ferramenta, a mensagem original do Zypper
é mantida.

## Falhas

Sem o marcador, a unit tenta de novo a cada 15 minutos (até 5 vezes em 3 horas)
e depois no próximo boot. O Zypper espera até 10 minutos por um lock
(`ZYPP_LOCK_TIMEOUT=600`) mantido pelo Vega ou por outra ferramenta.

```sh
journalctl -u vegad-first-update.service
# repetir manualmente (ou optar pela preparação em uma instalação dispensada)
sudo rm -f /var/lib/vega/first-update.done /var/lib/vega/first-update.skipped
sudo systemctl start vegad-first-update.service
```

## Chaves fixadas

SUSE 16, openSUSE Project, openSUSE Backports (duas), SUSE/SuSE legadas,
`home:rodrigosbrito` (lyra/vega/fina) e Packman. Ao trocar uma chave, atualize
o keyring e a lista em Go juntos; `TestShippedKeyringMatchesPinnedFingerprints`
falha se divergirem.

## Manutenção das chaves após o primeiro boot

`vegad-trusted-keys.service` executa `vegad sync-trusted-keys` em cada boot
instalado do Lyra OS Desktop e é enfileirado pelo RPM após instalação ou
upgrade. O scriptlet usa `systemctl --no-block start`: não espera pela
importação enquanto a própria transação RPM pode estar segurando o banco.
Uma falha é tentada novamente após cinco minutos, com limite de cinco
inícios por hora; o próximo boot também tenta novamente.

Essa rotina é independente de `first-update.done` e `first-update.skipped`.
Ela verifica o conjunto exato de fingerprints em toda execução e importa o
keyring autorizado. Reimportar chaves já presentes é idempotente no RPM;
não é necessário um marcador de versão que possa ficar desatualizado em
relação ao banco. Não há refresh, instalação de pacotes nem alteração dos
marcadores da preparação. A sessão live e o perfil server são excluídos.

### Rotação e retenção

Para introduzir uma chave, publique o keyring e a lista de fingerprints na
mesma versão do vegad, antes de o repositório depender exclusivamente da
nova chave. Durante a transição, mantenha ambas as chaves no conjunto
aprovado; posteriormente, a antiga pode ser retirada dos dois arquivos.

A manutenção é aditiva: retirar uma chave do keyring impede novas
importações dessa chave, mas **não a remove do banco RPM**. Chaves antigas
ou adicionadas pelo administrador podem atender outros repositórios e são
preservadas. Revogação/remoção exige uma migração específica e revisada;
remover uma entrada da lista não constitui revogação. Um downgrade também
não apaga chaves previamente importadas.

Diagnóstico e nova tentativa:

```sh
journalctl -u vegad-trusted-keys.service
sudo systemctl restart vegad-trusted-keys.service
```

## Estado persistente e nova tentativa

A interface D-Bus `org.lyraos.Vega1.Preparation`, no objeto
`/org/lyraos/Vega1`, oferece:

- `GetStatus() → (ssssssb)`: `State`, `Phase`, `ErrorKind`, `LastError`,
  `UpdatedAt`, `NextRetryAt`, `CanRetry`, nessa ordem.
- `Retry()`: exige a ação Polkit `org.lyraos.vega.software.manage-repos`.
  Solicita o início do serviço sem bloquear a chamada nem interromper uma
  execução que tenha começado em paralelo. Não remove marcadores de
  conclusão ou dispensa. O retorno indica que o pedido foi aceito; acompanhe
  `GetStatus` para saber o resultado.

O cliente pode consultar `GetStatus` a cada cinco segundos enquanto a página
estiver visível, inclusive antes de qualquer operação recusada. Essa interface
é aditiva e não altera o contrato de contagem `GetUpdateStatus`. A integração
visual e os tipos do cliente são acompanhados na [issue Vega #153](https://github.com/lyra-os-linux/vega/issues/153). Clientes
antigos continuam funcionando; a apresentação visual exige integração no
cliente e deve tolerar `UnknownInterface` ao conectar a um daemon antigo.

| Estado | Significado |
| --- | --- |
| `pending` | Preparação ainda não iniciada |
| `running` | Serviço em execução; a fase identifica a etapa |
| `waiting-retry` | O systemd tem uma nova tentativa agendada |
| `awaiting-approval` | Foi detectada uma chave não confiada; esperar não a autoriza |
| `failed` | Falha ou interrupção sem nova tentativa confirmada |
| `completed` | Marcador de conclusão presente |
| `skipped` | Instalação antiga dispensada |
| `unavailable` | Perfil, ambiente ou serviço não permite essa rotina |

Fases: `detecting-system`, `importing-keys`, `refreshing`, `listing-updates`,
`publishing`, `completed` ou `skipped`. `ErrorKind` distingue `network`,
`untrusted-key`, `operation-failed`, `interrupted` e `service-failed`.
A classificação de rede reconhece mensagens conhecidas do Zypper em locale
C; erros não reconhecidos permanecem genéricos. O diagnóstico público é um
resumo sem URLs ou credenciais; a saída detalhada fica no journal.

O arquivo de resumo público `/var/lib/vega/first-update-status.json` preserva fase,
diagnóstico e horário entre processos e reinicializações. O serviço D-Bus
combina esses dados com os marcadores e o estado atual do systemd para não
mostrar uma execução interrompida como ainda ativa. `NextRetryAt` é uma
estimativa UTC baseada nos quinze minutos de `RestartSec`, exibida somente
quando o systemd confirma uma tentativa automática pendente. O horário não
é uma garantia de sucesso. `CanRetry` informa se a ação pode ser oferecida;
a autorização ainda é verificada quando ela é chamada.

### Revisão de chaves desconhecidas

Após uma rejeição de chave no refresh global, a preparação consulta os aliases
habilitados e identifica a primeira proposta revisável. O caminho sem falhas
continua fazendo apenas um refresh. A proposta fica em
`/var/lib/vega/preparation-key.json`, com alias, fingerprint completo, assinante
e um token vinculado ao fingerprint e ao digest da configuração do repositório.
URLs e credenciais não são persistidas nesse arquivo público. Não depende de
um sinal emitido antes de a sessão do usuário existir.

- `GetPendingKeys() → a(ssss)` retorna propostas com `Repo`, `Fingerprint`,
  `UserID`, `Token`, nessa ordem, enquanto o estado exige aprovação.
- `ApproveKey(repo, fingerprint, token) → u` exige Polkit
  `org.lyraos.vega.software.manage-repos`. O retorno é um ID acompanhado pelos
  sinais normais de transação da interface `Software`.

O painel do Vega oferece **Revisar chave…** e apresenta alias, assinante e
fingerprint completo. Cancelar não modifica a confiança. Confirmar solicita
a autorização e confere novamente a identidade do repositório. O respondedor
estrito já usado por `TrustRepoKey` aceita somente o fingerprint completo
revisado, com identidade conferida também imediatamente antes da importação;
outra chave ou origem exige nova revisão. Não há autoimportação geral nem
remoção/adição do repositório para resolver esse estado.

Uma proposta antiga é recusada mesmo que a nova origem use o mesmo fingerprint.
Após uma falha dessa verificação o daemon tenta publicar a proposta atualizada,
para um novo diálogo. Após sucesso, remove a proposta e solicita nova tentativa
da preparação sem interromper uma execução concorrente. Se houver outras
chaves desconhecidas, elas serão apresentadas na próxima tentativa.

A consulta é compatível com daemons sem os novos métodos: nesse caso o cliente
mantém o diagnóstico e a ação de nova tentativa, sem oferecer revisão.

### Qualificação da revisão de chave em VM

`scripts/check-preparation-key-vm.py` usa o mesmo ambiente descartável da
qualificação abaixo, com um executável Zypper controlado que emite prompts XML.
Verifica persistência após reinício, consulta por usuário comum, recusa do
Polkit, token antigo, mudança de URL e aprovação que retoma a preparação.
O teste não importa uma chave no host nem usa repositórios de rede. A conferência
criptográfica do Zypper real continua coberta pelo teste opcional
`TestZypperKeyApprovalWithIsolatedRoot`.

```sh
mkdir -p /tmp/lyra-preparation-key-vm
go build -o /tmp/lyra-preparation-key-vm/vegad ./cmd/vegad
go test -c -o /tmp/lyra-preparation-key-vm/tests ./internal/dbusserver
python3 scripts/check-preparation-key-vm.py build
python3 scripts/check-preparation-key-vm.py run
```

O log fica em `/tmp/lyra-preparation-key-vm/serial.log`. Use `pack` antes de
`run` para atualizar somente os binários num ambiente já criado.

### Qualificação da nova tentativa em VM

`scripts/check-preparation-vm.py` monta um initramfs descartável com systemd,
D-Bus e Polkit reais, sem rede nem discos do host. O serviço de preparação
é uma tarefa controlada, sem acesso a repositórios. O teste verifica recusa
sem autorização, autorização para um usuário comum, retomada de unidade
inativa e falha, proteção de execução ativa e leitura do diagnóstico após
reinício do daemon.

```sh
mkdir -p /tmp/lyra-preparation-vm
go build -o /tmp/lyra-preparation-vm/vegad ./cmd/vegad
go test -c -o /tmp/lyra-preparation-vm/tests ./internal/dbusserver
python3 scripts/check-preparation-vm.py build
python3 scripts/check-preparation-vm.py run
```

O script usa as ferramentas locais do openSUSE e o kernel indicado em
`KERNEL`. O log fica em `/tmp/lyra-preparation-vm/serial.log`. Para repetir
com novos binários no mesmo ambiente, use `pack` antes de `run`.

## Parada e desligamento

`first-update` trata SIGTERM e SIGINT. Ao receber o pedido, deixa de iniciar
comandos e etapas, inclusive a publicação do marcador de conclusão. O comando
já iniciado tem até 30 segundos para terminar sua escrita de metadados ou
importação das chaves RPM. Se não terminar, o coordenador envia SIGTERM ao grupo
de processos e, após mais cinco segundos, SIGKILL. Descendentes remanescentes
do comando interrompido também são encerrados.

A unidade usa `KillMode=mixed`: o SIGTERM inicial chega somente ao coordenador.
`TimeoutStopSec=45s` fornece o limite externo para o systemd encerrar o cgroup
inteiro se a coordenação não concluir. Esse prazo não garante que uma escrita
termine; substitui o antigo limite de quinze minutos, que com o modo padrão
não adiava o SIGTERM enviado aos subprocessos.

A rotina não instala pacotes nem executa scriptlets de atualização. Ela não
adquire o inibidor `delay` do logind: esse inibidor é limitado por
`InhibitDelayMaxSec` e não cobre toda a drenagem nem substitui a coordenação da
unidade. As transações interativas do daemon mantêm sua política separada.

Uma interrupção tratada persiste `failed/interrupted` e não deixa o marcador
`first-update.done`. A próxima execução retoma a preparação com os metadados
existentes. Se houver encerramento forçado antes da persistência, a consulta
de estado reconcilia o antigo `running` com o serviço parado e informa a
interrupção. Uma parada explícita não agenda reinício durante o desligamento;
o serviço habilitado volta a ser elegível no próximo boot.

### Qualificação de parada

O teste usa a unidade empacotada, systemd real e executáveis controlados em VM
sem rede/discos do host. Verifica drenagem durante importação, encerramento de
subprocesso e descendente que ignoram SIGTERM, ausência do marcador, estado
retomável e conclusão numa execução posterior sem limpar manualmente o estado.

```sh
mkdir -p /tmp/lyra-preparation-stop-vm
go build -o /tmp/lyra-preparation-stop-vm/vegad ./cmd/vegad
go test -c -o /tmp/lyra-preparation-stop-vm/tests ./internal/dbusserver
python3 scripts/check-preparation-stop-vm.py build
python3 scripts/check-preparation-stop-vm.py run
```

O log fica em `/tmp/lyra-preparation-stop-vm/serial.log`. Os testes Go de
cancelamento também cobrem cada limite entre etapas e o comando cancelado
antes de começar. A VM valida a coordenação, não uma escrita RPM real interrompida.


## Política de snapshots

A preparação inicial, a manutenção das chaves fixadas e a aprovação de chaves
pendentes **não criam snapshots pre/post pelo Vega**. Elas não instalam nem
atualizam pacotes. O marcador `first-update.done` e o estado `completed`
afirmam somente que a preparação dos repositórios terminou; não afirmam que
existe um ponto de recuperação. A preparação também não depende de Snapper
estar instalado ou disponível.

As transações normais de instalação, remoção, atualização e limpeza de cache
nativas que usam `withSnapshots` seguem a política de **melhor esforço**:

| Situação | Comportamento |
| --- | --- |
| Snapshot pre criado | Executa a operação e tenta criar post vinculado ao ID pre |
| Snapper indisponível ou criação pre falha | Registra que seguirá sem snapshot pre do Vega, executa a operação e não tenta post |
| Operação falha após pre criado | Preserva a falha da operação e tenta post para registrar eventuais alterações parciais |
| Criação post falha | Registra o par incompleto e o ID pre; preserva o resultado da operação |

O resultado da transação e sua mensagem de conclusão descrevem a operação
solicitada, não a disponibilidade de snapshots. Os logs só anunciam um snapshot
criado quando sua criação retorna sucesso. Não há rollback automático nem
promessa de recuperação: cobertura dos subvolumes, retenção e disponibilidade
dos snapshots precisam ser verificadas separadamente. Snapshots eventualmente
criados por ferramentas externas não fazem parte dessa garantia do Vega.

Esta política é intencional: falhar ao criar um snapshot não bloqueia a
transação normal. Uma futura rotina que instale pacotes automaticamente deve
reavaliar explicitamente essa decisão antes de reutilizar o wrapper.
