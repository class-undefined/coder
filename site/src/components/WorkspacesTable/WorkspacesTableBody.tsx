import Button from "@mui/material/Button"
import { makeStyles } from "@mui/styles"
import AddOutlined from "@mui/icons-material/AddOutlined"
import { Workspace } from "api/typesGenerated"
import { ChooseOne, Cond } from "components/Conditionals/ChooseOne"
import { TableEmpty } from "components/TableEmpty/TableEmpty"
import { FC } from "react"
import { useTranslation } from "react-i18next"
import { Link as RouterLink } from "react-router-dom"
import { TableLoaderSkeleton } from "../TableLoader/TableLoader"
import { WorkspacesRow } from "./WorkspacesRow"

interface TableBodyProps {
  workspaces?: Workspace[]
  isUsingFilter: boolean
  onUpdateWorkspace: (workspace: Workspace) => void
  error?: Error | unknown
}

export const WorkspacesTableBody: FC<
  React.PropsWithChildren<TableBodyProps>
> = ({ workspaces, isUsingFilter, onUpdateWorkspace, error }) => {
  const { t } = useTranslation("workspacesPage")
  const styles = useStyles()

  if (error) {
    return <TableEmpty message={t("emptyResultsMessage")} />
  }

  if (!workspaces) {
    return <TableLoaderSkeleton columns={5} useAvatarData />
  }

  if (workspaces.length === 0) {
    return (
      <ChooseOne>
        <Cond condition={isUsingFilter}>
          <TableEmpty message={t("emptyResultsMessage")} />
        </Cond>

        <Cond>
          <TableEmpty
            className={styles.withImage}
            message={t("emptyCreateWorkspaceMessage")}
            description={t("emptyCreateWorkspaceDescription")}
            cta={
              <Button
                component={RouterLink}
                to="/templates"
                startIcon={<AddOutlined />}
                variant="contained"
              >
                {t("createFromTemplateButton")}
              </Button>
            }
            image={
              <div className={styles.emptyImage}>
                <img src="/featured/workspaces.webp" alt="" />
              </div>
            }
          />
        </Cond>
      </ChooseOne>
    )
  }

  return (
    <>
      {workspaces.map((workspace) => (
        <WorkspacesRow
          workspace={workspace}
          key={workspace.id}
          onUpdateWorkspace={onUpdateWorkspace}
        />
      ))}
    </>
  )
}

const useStyles = makeStyles((theme) => ({
  withImage: {
    paddingBottom: 0,
  },
  emptyImage: {
    maxWidth: "50%",
    height: theme.spacing(34),
    overflow: "hidden",
    marginTop: theme.spacing(6),
    opacity: 0.85,

    "& img": {
      maxWidth: "100%",
    },
  },
}))
