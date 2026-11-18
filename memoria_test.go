package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestCriarMemoriaSemCategoriasObrigatorias(t *testing.T) {
	for _, caminho := range []string{"Inicio.md", "Projetos/app/decisoes.md", "Referencias/regex.md", "Pasta livre/Subpasta/nota.md"} {
		t.Run(caminho, func(t *testing.T) {
			vault := t.TempDir()
			memoria := &memoriaVault{caminho: vault}
			conteudo := "# Nota\n\nMemória genérica."
			request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
				"path": caminho, "content": conteudo,
			}}}
			resultado, err := memoria.handler("memory_create")(context.Background(), request)
			if err != nil || resultado == nil || resultado.IsError {
				t.Fatalf("criação falhou: %+v, %v", resultado, err)
			}
			dados, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(caminho)))
			if err != nil || string(dados) != conteudo {
				t.Fatalf("conteúdo inesperado: %q, %v", dados, err)
			}
			for _, pasta := range []string{"Estudos", "LinkedIn"} {
				if _, err := os.Stat(filepath.Join(vault, pasta)); !os.IsNotExist(err) {
					t.Fatalf("categoria não solicitada criada: %s, %v", pasta, err)
				}
			}
			resultado, err = memoria.handler("memory_create")(context.Background(), request)
			if err != nil || resultado == nil || !resultado.IsError {
				t.Fatalf("deveria recusar sobrescrita: %+v, %v", resultado, err)
			}
		})
	}
}

func TestCriarMemoriaMantemProtecoes(t *testing.T) {
	for _, caminho := range []string{"../fora.md", "/fora.md", ".obsidian/nota.md", "Projetos/.obsidian/nota.md", "nota.txt"} {
		t.Run(caminho, func(t *testing.T) {
			memoria := &memoriaVault{caminho: t.TempDir()}
			request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
				"path": caminho, "content": "Não deve ser gravado",
			}}}
			resultado, err := memoria.handler("memory_create")(context.Background(), request)
			if err != nil || resultado == nil || !resultado.IsError {
				t.Fatalf("deveria rejeitar caminho: %+v, %v", resultado, err)
			}
		})
	}
}
