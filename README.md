# gitscope

Explorador interativo do histórico de um repositório git. Indexa os commits num
SQLite local e abre uma interface web no navegador para navegar, filtrar e comparar.

Um binário só, sem dependências externas além do `git`. Nada sai da sua máquina:
o servidor escuta apenas em `127.0.0.1`.

## Uso

```bash
go build -o gitscope ./cmd/gitscope

cd ~/Projects/algum-repo
gitscope                 # indexa e abre http://localhost:7777
gitscope index           # só atualiza o índice
gitscope --repo ../outro --port 8080 --no-open
```

O índice fica em `.git/gitscope/index.db` (fora do versionamento). A primeira
indexação lê todo o histórico; as seguintes só processam commits novos e removem
os que deixaram de existir (rebase, branch apagada).

## Interface

- **Filtros globais**: branch, autores, período (presets, datas ou arrastando na
  linha do tempo), prefixo de caminho, texto da mensagem e inclusão de merges.
  Todos se combinam, aparecem como chips removíveis e ficam na URL (dá para
  compartilhar ou voltar a uma visão).
- **Resumo**: commits, autores, linhas adicionadas e removidas, arquivos tocados.
- **Linha do tempo**: commits por dia, semana ou mês, conforme o período. Clique ou
  arraste para filtrar; duplo clique para voltar.
- **Commits**: lista paginada; clique para ver arquivos e diff.
- **Autores**: atividade por pessoa; clique para filtrar por ela.
- **Arquivos**: os mais alterados no filtro atual (primeira versão de hotspots);
  arquivos já removidos aparecem riscados. Clique para filtrar pelo caminho.

Com filtro de caminho, as contagens de linhas consideram só os arquivos daquele caminho.

## Estrutura

```
cmd/gitscope     CLI: indexa e sobe o servidor
internal/gitlog  execução e parsing do git
internal/store   SQLite: sincronização incremental e consultas com filtros
internal/server  API JSON + arquivos estáticos
web/             interface (HTML/CSS/JS sem build, embutida no binário)
```

## Próximos passos

1. Bisect interativo (visual, com comando automático e bisect por métrica)
2. Hotspots com complexidade e bus factor por diretório
3. Comparação lado a lado entre períodos, branches e grupos de autores
