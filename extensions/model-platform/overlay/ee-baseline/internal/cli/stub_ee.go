//go:build ee

package cli

import "errors"

func (c *CLI) AdminListUserAgentsCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("listing user agents is only available in the enterprise edition")
}

func (p *Parser) parseAdminListUserAgentsEE(userName string) (*Command, error) {
	return nil, errors.New("listing user agents is only available in the enterprise edition")
}

func (c *CLI) AdminListUserDatasetsCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("listing user datasets is only available in the enterprise edition")
}

func (p *Parser) parseAdminListUserDatasetsEE(userName string) (*Command, error) {
	return nil, errors.New("listing user datasets is only available in the enterprise edition")
}

func (c *CLI) ListAdminIngestionTasks(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("listing admin ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminListIngestionTasks() (*Command, error) {
	return nil, errors.New("listing admin ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminShowIngestionLog() (*Command, error) {
	return nil, errors.New("ingestion logs are only available in the enterprise edition")
}

func (c *CLI) AdminShowIngestionLogCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("ingestion logs are only available in the enterprise edition")
}

func (c *CLI) AdminShowIngestionMessagesCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("ingestion logs are only available in the enterprise edition")
}

func (c *CLI) AdminListUserIngestionTasksCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("listing user ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminListUserIngestionTasks(userName string) (*Command, error) {
	return nil, errors.New("listing user ingestion tasks is only available in the enterprise edition")
}

func (c *CLI) AdminStopIngestionCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("stopping admin ingestion tasks is only available in the enterprise edition")
}

func (c *CLI) AdminRemoveIngestionCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("removing admin ingestion tasks is only available in the enterprise edition")
}

func (c *CLI) AdminStopUserIngestionTasksCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("stopping user ingestion tasks is only available in the enterprise edition")
}

func (c *CLI) AdminRemoveUserIngestionTasksCommand(commandCount int, cmd *Command) (ResponseIf, error) {
	return nil, errors.New("removing user ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminStopIngestionTasks() (*Command, error) {
	return nil, errors.New("stopping admin ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminRemoveIngestionTasks() (*Command, error) {
	return nil, errors.New("removing admin ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminStopUserIngestionTasks(userName string) (*Command, error) {
	return nil, errors.New("stopping user ingestion tasks is only available in the enterprise edition")
}

func (p *Parser) parseAdminRemoveUserIngestionTasks(userName string) (*Command, error) {
	return nil, errors.New("removing user ingestion tasks is only available in the enterprise edition")
}
