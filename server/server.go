package server

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

var p *tea.Program

func StartServer() {
	m := initModel()
	p = tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Println("Error starting TUI:", err)
		os.Exit(1)
	}
}

type model struct {
	table   table.Model
	packets []table.Row
	counter int
	mu      sync.Mutex
}

type Msg struct {
	Data string
}

func initModel() tea.Model {
	//TODO these columns should be dynamic (need DB for that)
	columns := []table.Column{
		{Title: "ID", Width: 5},
		{Title: "Message", Width: 50000},
	}

	m := model{
		table: table.New(
			table.WithColumns(columns),
			table.WithFocused(true),
		),
		packets: []table.Row{},
	}

	m.table.SetStyles(table.DefaultStyles())

	m.table.SetRows(m.packets)

	return m
}

func (m model) Init() tea.Cmd {
	return socketSetup(&m)
}

// TODO ugh I'm copying a lock RIP, should work fine for now but still badbadbad
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "down":
			m.table.MoveDown(1)
			return m, nil
		case "up":
			m.table.MoveUp(1)
			return m, nil
		case "enter":
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(msg.Height)
		m.table.Columns()[1].Width = msg.Width - 10
	case Msg:
		m.mu.Lock()
		m.packets = append([]table.Row{table.Row{fmt.Sprintf("%d", len(m.packets)+1), msg.Data}}, m.packets...)
		m.mu.Unlock()
		m.table.SetRows(m.packets)
	}

	return m, nil
}

func (m model) View() string {
	return m.table.View()
}

func socketSetup(m tea.Model) tea.Cmd {
	return func() tea.Msg {
		listener, err := net.Listen("tcp", "127.0.0.1:8080")
		if err != nil {
			log.Fatal(err)
		}
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go handleConnection(conn, m.(model))
		}
	}
}

func handleConnection(conn net.Conn, m model) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		m.counter = m.counter + 1
		data := scanner.Text()
		p.Send(Msg{Data: data})
	}
}
