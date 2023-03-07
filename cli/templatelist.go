package cli

import (
	"fmt"

	"github.com/fatih/color"

	"github.com/coder/coder/cli/clibase"
	"github.com/coder/coder/cli/cliui"
)

func templateList() *clibase.Command {
	formatter := cliui.NewOutputFormatter(
		cliui.TableFormat([]templateTableRow{}, []string{"name", "last updated", "used by"}),
		cliui.JSONFormat(),
	)

	cmd := &clibase.Command{
		Use:     "list",
		Short:   "List all the templates available for the organization",
		Aliases: []string{"ls"},
		Handler: func(inv *clibase.Invokation) error {
			client, err := useClient(cmd)
			if err != nil {
				return err
			}
			organization, err := CurrentOrganization(cmd, client)
			if err != nil {
				return err
			}
			templates, err := client.TemplatesByOrganization(inv.Context(), organization.ID)
			if err != nil {
				return err
			}

			if len(templates) == 0 {
				_, _ = fmt.Fprintf(inv.Stderr, "%s No templates found in %s! Create one:\n\n", Caret, color.HiWhiteString(organization.Name))
				_, _ = fmt.Fprintln(inv.Stderr, color.HiMagentaString("  $ coder templates create <directory>\n"))
				return nil
			}

			rows := templatesToRows(templates...)
			out, err := formatter.Format(inv.Context(), rows)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(cmd.OutOrStdout(), out)
			return err
		},
	}

	formatter.AttachFlags(cmd)
	return cmd
}
