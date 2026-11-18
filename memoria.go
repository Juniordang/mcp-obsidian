package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type memoriaVault struct {
	caminho string
	mu      sync.Mutex // Serializa operações desta instância do servidor.
}

func registrarFerramentasMemoria(s *server.MCPServer, memoria *memoriaVault) {
	ferramentas := []mcp.Tool{
		mcp.NewTool("memory_search",
			mcp.WithDescription("Busca textual no caminho, título e conteúdo das notas do Vault. Recupera memórias relevantes, retornando caminho, relevância e trechos."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Termos da busca")),
			mcp.WithNumber("limit", mcp.Min(1), mcp.Max(20), mcp.Description("Máximo de notas, inteiro de 1 a 20; padrão 8")),
		),
		mcp.NewTool("memory_read",
			mcp.WithDescription("Lê o conteúdo integral de uma nota Markdown pelo caminho relativo dentro do Vault."),
			mcp.WithString("path", mcp.Required()),
		),
		mcp.NewTool("memory_create",
			mcp.WithDescription("Cria uma nota .md na raiz ou em qualquer subpasta permitida do Vault. Falha se o arquivo já existir (use memory_update). Cria diretórios intermediários, sem impor pastas ou categorias."),
			mcp.WithString("path", mcp.Required(), mcp.Description("Caminho relativo .md, como Inicio.md ou Projetos/app/decisoes.md; respeite a organização do Vault")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Conteúdo Markdown da nota")),
		),
		mcp.NewTool("memory_update",
			mcp.WithDescription("Atualiza nota existente: append anexa texto; replace exige oldString não vazio ocorrendo exatamente uma vez."),
			mcp.WithString("path", mcp.Required()),
			mcp.WithString("mode", mcp.Required(), mcp.Enum("replace", "append")),
			mcp.WithString("oldString"),
			mcp.WithString("newString", mcp.Required()),
		),
		mcp.NewTool("memory_forget",
			mcp.WithDescription("Apaga permanentemente uma nota .md. Use apenas após pedido explícito do usuário; não apague automaticamente memórias antigas."),
			mcp.WithString("path", mcp.Required()),
		),
	}
	for _, ferramenta := range ferramentas {
		s.AddTool(ferramenta, memoria.handler(ferramenta.Name))
	}
}

func (m *memoriaVault) handler(nome string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		raiz, err := os.OpenRoot(m.caminho)
		if err != nil {
			return mcp.NewToolResultError("Não foi possível abrir o Vault: " + err.Error()), nil
		}
		defer raiz.Close()
		var texto string
		switch nome {
		case "memory_search":
			texto, err = buscarMemorias(ctx, raiz, request)
		case "memory_read":
			var caminho string
			caminho, err = caminhoNota(raiz, request)
			if err == nil {
				texto, err = lerMemoria(raiz, caminho)
			}
		case "memory_create":
			texto, err = criarMemoria(raiz, request)
		case "memory_update":
			texto, err = atualizarMemoria(raiz, request)
		case "memory_forget":
			var caminho string
			caminho, err = caminhoNota(raiz, request)
			if err == nil {
				err = raiz.Remove(caminho)
				texto = "Nota apagada: " + caminho
			}
		default:
			err = fmt.Errorf("ferramenta desconhecida: %s", nome)
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(texto), nil
	}
}

// Apenas caminhos relativos Markdown, sem acesso à área interna do Obsidian.
// os.Root confina as operações ao Vault também durante a resolução pelo SO.
func validarCaminhoNota(raiz *os.Root, nome string) error {
	if !fs.ValidPath(nome) || strings.ContainsAny(nome, "\\:\x00") || !strings.HasSuffix(nome, ".md") {
		return fmt.Errorf("informe um caminho relativo .md dentro do Vault, usando / entre pastas")
	}
	partes := strings.Split(nome, "/")
	for _, parte := range partes {
		if strings.EqualFold(parte, ".obsidian") {
			return fmt.Errorf("área interna .obsidian é protegida")
		}
	}
	for i := range partes {
		info, err := raiz.Lstat(strings.Join(partes[:i+1], "/"))
		if os.IsNotExist(err) {
			break // memory_create poderá criar os componentes que faltam.
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("links simbólicos não são permitidos")
		}
		if i < len(partes)-1 && !info.IsDir() {
			return fmt.Errorf("componente do caminho não é uma pasta")
		}
		if i == len(partes)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("apenas arquivos regulares .md são permitidos")
		}
	}
	return nil
}

func caminhoNota(raiz *os.Root, request mcp.CallToolRequest) (string, error) {
	nome, err := request.RequireString("path")
	if err != nil {
		return "", err
	}
	return nome, validarCaminhoNota(raiz, nome)
}

func lerMemoria(raiz *os.Root, nome string) (string, error) {
	if err := validarCaminhoNota(raiz, nome); err != nil {
		return "", err
	}
	conteudo, err := raiz.ReadFile(nome)
	return string(conteudo), err
}

func criarMemoria(raiz *os.Root, request mcp.CallToolRequest) (string, error) {
	nome, err := caminhoNota(raiz, request)
	if err != nil {
		return "", err
	}
	conteudo, err := request.RequireString("content")
	if err != nil {
		return "", err
	}
	if err := raiz.MkdirAll(path.Dir(nome), 0755); err != nil {
		return "", err
	}
	if err := validarCaminhoNota(raiz, nome); err != nil {
		return "", err
	}
	f, err := raiz.OpenFile(nome, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(err) {
		return "", fmt.Errorf("já existe: %s (use memory_update)", nome)
	}
	if err != nil {
		return "", err
	}
	_, escritaErr := io.WriteString(f, conteudo)
	fechamentoErr := f.Close()
	if escritaErr != nil || fechamentoErr != nil {
		raiz.Remove(nome)
		if escritaErr != nil {
			return "", escritaErr
		}
		return "", fechamentoErr
	}
	return "Nota criada: " + nome, nil
}

func atualizarMemoria(raiz *os.Root, request mcp.CallToolRequest) (string, error) {
	nome, err := caminhoNota(raiz, request)
	if err != nil {
		return "", err
	}
	modo, err := request.RequireString("mode")
	if err != nil {
		return "", err
	}
	novo, err := request.RequireString("newString")
	if err != nil {
		return "", err
	}
	atual, err := lerMemoria(raiz, nome)
	if err != nil {
		return "", err
	}
	var conteudo string
	switch modo {
	case "append":
		separador := ""
		if !strings.HasSuffix(atual, "\n") {
			separador = "\n"
		}
		conteudo = atual + separador + novo + "\n"
	case "replace":
		antigo, err := request.RequireString("oldString")
		if err != nil || antigo == "" {
			return "", fmt.Errorf("replace exige oldString não vazio")
		}
		if quantidade := strings.Count(atual, antigo); quantidade != 1 {
			return "", fmt.Errorf("oldString deve ocorrer exatamente 1 vez (encontrado %dx)", quantidade)
		}
		conteudo = strings.Replace(atual, antigo, novo, 1)
	default:
		return "", fmt.Errorf("mode inválido: use replace ou append")
	}
	info, err := raiz.Stat(nome)
	if err != nil {
		return "", err
	}
	temporario := path.Join(path.Dir(nome), ".ai-brain-"+rand.Text()+".tmp")
	f, err := raiz.OpenFile(temporario, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return "", err
	}
	defer raiz.Remove(temporario)
	_, escritaErr := io.WriteString(f, conteudo)
	fechamentoErr := f.Close()
	if escritaErr != nil {
		return "", escritaErr
	}
	if fechamentoErr != nil {
		return "", fechamentoErr
	}
	if err := validarCaminhoNota(raiz, nome); err != nil {
		return "", err
	}
	if err := raiz.Rename(temporario, nome); err != nil {
		return "", err
	}
	return "Nota atualizada: " + nome, nil
}

func buscarMemorias(ctx context.Context, raiz *os.Root, request mcp.CallToolRequest) (string, error) {
	consulta, err := request.RequireString("query")
	if err != nil || strings.TrimSpace(consulta) == "" {
		return "", fmt.Errorf("query deve ser um texto não vazio")
	}
	limite := 8
	if valor, existe := request.GetArguments()["limit"]; existe {
		var numero float64
		switch valor := valor.(type) {
		case float64:
			numero = valor
		case int:
			numero = float64(valor)
		default:
			return "", fmt.Errorf("limit deve ser um inteiro de 1 a 20")
		}
		if math.IsNaN(numero) || numero < 1 || numero > 20 || math.Trunc(numero) != numero {
			return "", fmt.Errorf("limit deve ser um inteiro de 1 a 20")
		}
		limite = int(numero)
	}
	termos := strings.Fields(strings.ToLower(consulta))
	type resultadoBusca struct {
		nome       string
		relevancia int
		trechos    []string
	}
	var resultados []resultadoBusca
	err = fs.WalkDir(raiz.FS(), ".", func(nome string, entrada fs.DirEntry, erro error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if erro != nil {
			return erro
		}
		if strings.EqualFold(entrada.Name(), ".obsidian") && entrada.IsDir() {
			return fs.SkipDir
		}
		if !entrada.Type().IsRegular() || !strings.HasSuffix(nome, ".md") {
			return nil
		}
		conteudo, err := lerMemoria(raiz, nome)
		if err != nil {
			return nil // Nota ilegível não impede a busca nas demais.
		}
		resultado := resultadoBusca{nome: nome}
		texto := strings.ToLower(nome + "\n" + conteudo)
		for _, termo := range termos {
			resultado.relevancia += strings.Count(texto, termo)
		}
		if resultado.relevancia == 0 {
			return nil
		}
		for i, linha := range strings.Split(conteudo, "\n") {
			if len(resultado.trechos) == 3 {
				break
			}
			for _, termo := range termos {
				if strings.Contains(strings.ToLower(linha), termo) && strings.TrimSpace(linha) != "" {
					trecho := []rune(strings.TrimSpace(linha))
					if len(trecho) > 160 {
						trecho = trecho[:160]
					}
					resultado.trechos = append(resultado.trechos, fmt.Sprintf("L%d: %s", i+1, string(trecho)))
					break
				}
			}
		}
		resultados = append(resultados, resultado)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(resultados, func(i, j int) bool {
		if resultados[i].relevancia != resultados[j].relevancia {
			return resultados[i].relevancia > resultados[j].relevancia
		}
		return resultados[i].nome < resultados[j].nome
	})
	if len(resultados) == 0 {
		return "Nenhuma memória encontrada.", nil
	}
	var saida []string
	for _, resultado := range resultados[:min(limite, len(resultados))] {
		saida = append(saida, fmt.Sprintf("## %s (relevância %d)\n%s", filepath.ToSlash(resultado.nome), resultado.relevancia, strings.Join(resultado.trechos, "\n")))
	}
	return strings.Join(saida, "\n\n"), nil
}
