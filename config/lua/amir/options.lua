--vim.opt.guicursor = 'n-v-c-sm:block-Cursor,i-ci-ve:ver25-Cursor,r-cr-o:hor20-Cursor'
--
vim.o.wrap = true
vim.opt.shiftwidth = 2
vim.opt.tabstop = 4
vim.opt.expandtab = true

vim.api.nvim_create_autocmd('VimEnter', {
  callback = function() vim.cmd 'windo set wrap' end,
})

-- Auto-set working directory to project root so that debugging,
-- test running, and file navigation work from any launch directory.
vim.api.nvim_create_autocmd('BufEnter', {
  pattern = '*.go',
  callback = function()
    local root = vim.fs.root(0, { 'go.mod', '.git' })
    if root then vim.cmd.cd(root) end
  end,
})

vim.keymap.set('n', '<leader>x', '<cmd>close<CR>', { desc = 'Close current window' })
vim.keymap.set('n', '<leader>rgt', ':terminal go test ./...<CR>', { desc = 'Run Go tests' })
vim.keymap.set('n', '<leader>rgr', ':terminal go run .<CR>', { desc = 'Run Go' })
vim.keymap.set('n', '<leader>dt', function() require('dap-go').debug_test() end, { desc = 'Debug: Test under cursor' })
vim.keymap.set('n', '<leader>rt', function()
  local node = vim.treesitter.get_node()
  while node do
    if node:type() == 'function_declaration' then
      local name = vim.treesitter.get_node_text(node:field('name')[1], 0)
      if name:match '^Test' then
        vim.cmd('split | terminal go test -run ' .. name .. ' -v ./')
        return
      end
    end
    node = node:parent()
  end
  vim.notify('No test function found under cursor', vim.log.levels.WARN)
end, { desc = 'Run test under cursor' })

vim.keymap.set('n', '<leader>ggtt', function()
  local file = vim.fn.expand '%:r'
  local ext = vim.fn.expand '%:e'
  local target

  if file:match '_test$' then
    target = file:gsub('_test$', '') .. '.' .. ext
  else
    target = file .. '_test.' .. ext
  end

  local is_new = vim.fn.filereadable(target) == 0
  vim.cmd('vsplit ' .. target)

  if is_new and target:match '_test%.go$' then
    local dir = vim.fn.expand '%:h:t' -- innermost directory name
    local template = string.format(
      [[package %s

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestThing(t *testing.T) {
	given := ""

	result := ""

	assert.Equal(t, given, result)
}]],
      dir
    )

    vim.api.nvim_buf_set_lines(0, 0, -1, false, vim.split(template, '\n'))
  end
end, { desc = 'Generate Test file and open in Split' })

vim.keymap.set('n', '<leader>ggtf', function()
  -- Find the enclosing function name using treesitter
  local node = vim.treesitter.get_node()
  local func_name = nil

  while node do
    if node:type() == 'function_declaration' then
      local name_node = node:field('name')[1]
      if name_node then func_name = vim.treesitter.get_node_text(name_node, 0) end
      break
    end
    node = node:parent()
  end

  if not func_name then
    vim.notify('No function found under cursor', vim.log.levels.WARN)
    return
  end

  -- Build the test function name
  local test_func_name = 'Test' .. func_name:sub(1, 1):upper() .. func_name:sub(2)

  local test_snippet = string.format(
    [[

func %s(t *testing.T) {
	given := ""

	result := %s()

	assert.Equal(t, given, result)
}]],
    test_func_name,
    func_name
  )

  -- Determine the test file
  local file = vim.fn.expand '%:r'
  local ext = vim.fn.expand '%:e'
  local target = file .. '_test.' .. ext
  local is_new = vim.fn.filereadable(target) == 0

  vim.cmd('vsplit ' .. target)

  if is_new then
    local dir = vim.fn.expand '%:h:t'
    local template = string.format(
      [[package %s

import (
	"github.com/stretchr/testify/assert"
	"testing"
)]],
      dir
    ) .. test_snippet

    vim.api.nvim_buf_set_lines(0, 0, -1, false, vim.split(template, '\n'))
  else
    -- Append to existing file
    local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false)
    local insert_lines = vim.split(test_snippet, '\n')
    vim.api.nvim_buf_set_lines(0, #lines, #lines, false, insert_lines)
    -- Jump to the new test function
    vim.cmd 'normal! G'
  end
end, { desc = 'Generate test for function under cursor' })
