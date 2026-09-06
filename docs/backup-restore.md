# Restauração de backups

`RestoreSnapshot` e `RestoreItems` extraem os dados com `restic restore --verify`
em uma área privada `.vega-restore-*` dentro do destino. O destino precisa de espaço
para os arquivos restaurados além dos arquivos atuais. Falhas de extração,
verificação ou sincronização não substituem os arquivos existentes.

No modo `replace`, arquivos e links restaurados substituem os itens correspondentes.
Diretórios existentes são mesclados, preservando seus metadados e os itens que não
fazem parte da restauração. Diretórios novos, arquivos e links mantêm os metadados
restaurados. Conflitos entre diretório e arquivo/link são recusados antes da aplicação;
nesse caso, escolha uma pasta separada. Uma pasta separada de uma restauração
anterior nunca é sobrescrita.

Antes da aplicação, `recovery.json` registra o destino e os caminhos relativos.
Cada original substituído é movido para `originals/` antes da entrada restaurada
ser movida de `new/` para o destino. Uma falha tenta desfazer as movimentações em
ordem inversa. Se a aplicação falhar, a área de trabalho é preservada e o erro
informa seu caminho, inclusive quando a compensação automática consegue terminar.

Após interrupção do processo, a área privada também permanece. Para recuperar,
pare as gravações no destino e examine `recovery.json`, `originals/`, `new/` e o
destino. Preserve uma cópia da área inteira antes de qualquer ação manual. Entradas
em `originals/` são as versões anteriores e correspondem aos mesmos caminhos
relativos no destino. A ausência de uma entrada em `originals/` não autoriza apagar
o destino: a movimentação pode não ter começado ou já ter sido desfeita. Não há
limpeza automática de áreas deixadas por execuções anteriores.

A substituição é recuperável por entrada, não uma troca atômica de toda a árvore.
Outros aplicativos devem parar de gravar nos arquivos escolhidos durante a operação.
Restaurações simultâneas do Vega para o mesmo diretório são recusadas. A travessia
dos diretórios durante a aplicação não segue links simbólicos.

Validação local: testes com falhas de extração/verificação, ENOSPC injetado em
cada movimentação, falha da compensação, conflitos de tipo, links, metadados e
seleção parcial; integração com repositório restic descartável.
