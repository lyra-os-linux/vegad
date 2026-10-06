# Atualização transacional de uma referência existente

ReferenceWriter avança um store de emissor **já inicializado**. Não provisiona
subvolume, diretório, lock, pin ou primeira referência; não adota temporários,
não reconstrói ownership de NSS, não escreve catálogo nem concede admissão.
O próximo gate de provisionamento continua aberto.

A biblioteca de primeira emissão está implementada em
[parental-reference-provisioning.md](parental-reference-provisioning.md), com
testes de interrupção de processo em armazenamento de fixture. Sua qualificação
em VM/Btrfs real e integração com a prova do instalador continuam pendentes.

Advance exige snapshot esperado completo, próximo RecoveryReference validado,
operation ID e callback de revalidação. A sequência aumenta exatamente uma unidade;
revisão do catálogo aumenta, instalação permanece igual, próximo modo é
blocked-recovery. Todos os proprietários/generações/identidades/classes/políticas/
adapters e operações pendentes devem ser preservados. Tombstones existentes são
idênticos e não podem desaparecer; esta API não transforma vivos em novos tombstones.
Novos registros precisam constar explicitamente no payload do emissor confiável.
Não é API para migração, mudança de policy, remoção terminal ou admissão positiva.

## Autoridade e autorização

Factory é root, configura diretórios absolutos canônicos, issuer fixo e qualifier
obrigatório. JSON/digest/issuer token não provam identidade criptográfica do emissor,
completude/frescor da lista ou autoridade de manutenção. Essas propriedades são
contratos do chamador confiável; payload de GUI/NSS/backup local não atende ao contrato.
Não há integração com DBus/guardian nem diálogo Polkit nesta etapa.

revalidate é obrigatório, bounded, read-only e precisa ser vinculado pelo chamador à
instalação, operação, caller, digests completos e autorização ainda válida. É chamado
antes de acessar storage e novamente sob EX, antes de preparar/gravar/concluir.
Qualquer diálogo Polkit deve ter terminado **antes** de adquirir EX. Não chamar esta
API sob SH do catálogo/Guard nem consultar esta própria fonte para revalidar sob EX.
Root/wheel não recebem capability implícita. Callback nil/negativo não autoriza.
Os callbacks de autorização/emissão do laboratório são fixtures explícitas.

## Protocolo de disco

O writer exige reference.lock existente, estável, 0600/root/single-link, EX não
bloqueante no mesmo inode dos leitores. Store/path ancestry, root, qualifier e
escritores cooperantes são trusted. Não cria/repara locks ou registros ausentes.
Revalida o domínio antes/depois com SHA256 witness; erro/mudança recusa.

1. Conferir reference.json/current.json seguros e correspondentes ao esperado.
2. Preparar journal imutável .reference-transaction.json contendo schema1, issuer,
   operação e referências completas before/after. Decoder fechado, limite
   2*ReferenceLimit+1024, duplicatas/unknown/null/bad types recusam.
3. Temp privado0600/random/O_EXCL, write/fsync/close, rename NOREPLACE do journal,
   fsync diretório. Cada substituição confere os bytes/inode anteriores sob EX.
4. Publicar a nova reference.json por temp/fsync/rename/fsync-dir; depois o novo pin
   current.json com o mesmo protocolo. A sequência nunca é reduzida.
5. Conferir pair/journal/lease/domínio/autorização novamente. Sync diretório,
   remover journal, sync diretório antes de devolver sucesso. Lock nunca é substituído.

FileReferenceSource agora recusa **qualquer** entrada pending sob esse nome, inclusive
symlink/FIFO/corrompida, antes/depois da leitura sob SH. Não segue, interpreta ou limpa
journal, não retorna o par antigo enquanto existe transação pendente. Escritor vivo
mantendo EX produz Busy. Temp órfão fora do nome canônico não é adotado.

## Falhas e retomada

Antes de publicar journal, falhas preservam o par anterior. Após rename do journal,
erros devolvem PublicationUncertain/resultado vazio; a referência não é revertida.
Falha/crash com journal deixa leitores recusando, inclusive se os dois arquivos já
contêm a nova versão. Após unlink do journal, o par novo já foi sincronizado; erro
de último fsync não autoriza reexecutar Advance com snapshot antigo.

Resume exige nova autorização para **exatamente** o next reference/operation do
journal. Aceita somente pares alcançáveis old/old, new/old e new/new. old/new,
conteúdo desconhecido, pin de outro issuer, ausência/unsafe de registros ou journal
inválido recusam; não recriam arquivos nem rebaixam pin. Continua só em direção a new,
confere dados e limpa journal após revalidação. Advance jamais resume automaticamente.
Replay de Advance antigo conflita; Resume sem journal exige recuperação separada.

Arquivos/qualifier/root são trusted: não é garantia contra root hostil, ABA
intencional ou restauração conjunta de journal/pin/ref/VM/filesystem. Não há contador
monotônico global. Fsync/temp bounds dependem de filesystem/kernel operacional.
Crash que deixa journal inválido exige manutenção externa com authority qualificada,
nunca reconstrução permissiva por ausência.

## Evidências e limites

Testes cobrem CAS/replay, EX/revalidação/revogação, ausência/unsafe, decoder/ownership,
falhas de oito fsyncs e cortes simulados nos estágios. [Ensaio root em VM](evidence/parental-reference-writer/README.md)
usa Btrfs real, factory root e três processos terminados com os.Exit73 após journal,
referência ou pin duráveis. Leitor recusou, nil/wrong-op recusaram, retomada exata
concluiu, mesmo lock permaneceu e catálogo não mudou. Isso é corte de processo,
**não** ensaio de perda física de energia, reboot/GRUB ou crash do kernel.

Próximo: provisionamento de primeira emissão com comprovante de instalação nova e
selo durável que impeça reinicialização após perda de pin/ref; depois integração de
maintenance/caller/Polkit, emissor completo e publisher de recuperação bloqueada.
Gates boot/Snapper/persistência/ISO/MAC permanecem abertos. Não habilitar/instalar
parental na estação com base no writer ou nas fixtures deste ensaio.
