package session

import (
	"fmt"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/creative"
	"github.com/df-mc/dragonfly/server/item/inventory"
	"github.com/df-mc/dragonfly/server/item/recipe"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/RadiatedMonkey/gophertunnel/minecraft/protocol"
	"golang.org/x/exp/slices"
	"math"
)

// handleCraft handles the CraftRecipe request action.
func (h *ItemStackRequestHandler) handleCraft(a *protocol.CraftRecipeStackRequestAction, s *Session) error {
	craft, ok := s.recipes[a.RecipeNetworkID]
	if !ok {
		return fmt.Errorf("recipe with network id %v does not exist", a.RecipeNetworkID)
	}
	_, shaped := craft.(recipe.Shaped)
	_, shapeless := craft.(recipe.Shapeless)
	if !shaped && !shapeless {
		return fmt.Errorf("recipe with network id %v is not a shaped or shapeless recipe", a.RecipeNetworkID)
	}
	if craft.Block() != "crafting_table" {
		return fmt.Errorf("recipe with network id %v is not a crafting table recipe", a.RecipeNetworkID)
	}

	size := s.craftingSize()
	offset := s.craftingOffset()
	consumed := make([]bool, size)
	for _, expected := range craft.Input() {
		var processed bool
		for slot := offset; slot < offset+size; slot++ {
			if consumed[slot-offset] {
				// We've already consumed this slot, skip it.
				continue
			}
			has, _ := s.ui.Item(int(slot))
			if has.Empty() != expected.Empty() || has.Count() < expected.Count() {
				// We can't process this item, as it's not a part of the recipe.
				continue
			}
			if !matchingStacks(has, expected) {
				// Not the same item.
				continue
			}
			processed, consumed[slot-offset] = true, true
			st := has.Grow(-expected.Count())
			h.setItemInSlot(protocol.StackRequestSlotInfo{
				ContainerID: protocol.ContainerCraftingInput,
				Slot:        byte(slot),
			}, st, s)
			break
		}
		if !processed {
			return fmt.Errorf("recipe %v: could not consume expected item: %v", a.RecipeNetworkID, expected)
		}
	}
	return h.createResults(s, craft.Output()...)
}

// handleAutoCraft handles the AutoCraftRecipe request action.
func (h *ItemStackRequestHandler) handleAutoCraft(a *protocol.AutoCraftRecipeStackRequestAction, s *Session) error {
	craft, ok := s.recipes[a.RecipeNetworkID]
	if !ok {
		return fmt.Errorf("recipe with network id %v does not exist", a.RecipeNetworkID)
	}
	_, shaped := craft.(recipe.Shaped)
	_, shapeless := craft.(recipe.Shapeless)
	if !shaped && !shapeless {
		return fmt.Errorf("recipe with network id %v is not a shaped or shapeless recipe", a.RecipeNetworkID)
	}
	if craft.Block() != "crafting_table" {
		return fmt.Errorf("recipe with network id %v is not a crafting table recipe", a.RecipeNetworkID)
	}

	repetitions := int(a.TimesCrafted)
	input := make([]item.Stack, 0, len(craft.Input()))
	for _, i := range craft.Input() {
		input = append(input, i.Grow(i.Count()*(repetitions-1)))
	}

	flattenedInputs := make([]item.Stack, 0, len(input))
	for _, i := range input {
		if i.Empty() {
			// We don't actually need this item - it's empty, so avoid putting it in our flattened inputs.
			continue
		}

		if ind := slices.IndexFunc(flattenedInputs, func(st item.Stack) bool {
			return matchingStacks(st, i)
		}); ind >= 0 {
			i = i.Grow(flattenedInputs[ind].Count())
			flattenedInputs = slices.Delete(flattenedInputs, ind, ind+1)
		}
		flattenedInputs = append(flattenedInputs, i)
	}

	for _, expected := range flattenedInputs {
		for id, inv := range map[byte]*inventory.Inventory{
			protocol.ContainerCraftingInput:              s.ui,
			protocol.ContainerCombinedHotBarAndInventory: s.inv,
		} {
			for slot, has := range inv.Slots() {
				if has.Empty() {
					// We don't have this item, skip it.
					continue
				}
				if !matchingStacks(has, expected) {
					// Not the same item.
					continue
				}

				remaining, removal := expected.Count(), has.Count()
				if remaining < removal {
					removal = remaining
				}

				expected, has = expected.Grow(-removal), has.Grow(-removal)
				h.setItemInSlot(protocol.StackRequestSlotInfo{
					ContainerID: id,
					Slot:        byte(slot),
				}, has, s)
				if expected.Empty() {
					// Consumed this item, so go to the next one.
					break
				}
			}
			if expected.Empty() {
				// Consumed this item, so go to the next one.
				break
			}
		}
		if !expected.Empty() {
			return fmt.Errorf("recipe %v: could not consume expected item: %v", a.RecipeNetworkID, expected)
		}
	}

	output := make([]item.Stack, 0, len(craft.Output()))
	for _, o := range craft.Output() {
		count, maxCount := o.Count(), o.MaxCount()
		total := count * repetitions

		stacks := int(math.Ceil(float64(total) / float64(maxCount)))
		for i := 0; i < stacks; i++ {
			inc := min(total, maxCount)
			total -= inc

			output = append(output, o.Grow(inc-count))
		}
	}
	return h.createResults(s, output...)
}

// handleCreativeCraft handles the CreativeCraft request action.
func (h *ItemStackRequestHandler) handleCreativeCraft(a *protocol.CraftCreativeStackRequestAction, s *Session) error {
	if !s.c.GameMode().CreativeInventory() {
		return fmt.Errorf("can only craft creative items in gamemode creative/spectator")
	}
	index := a.CreativeItemNetworkID - 1
	if int(index) >= len(creative.Items()) {
		return fmt.Errorf("creative item with network ID %v does not exist", index)
	}
	it := creative.Items()[index]
	it = it.Grow(it.MaxCount() - 1)
	return h.createResults(s, it)
}

// craftingSize gets the crafting size based on the opened container ID.
func (s *Session) craftingSize() uint32 {
	if s.openedContainerID.Load() == 1 {
		return craftingGridSizeLarge
	}
	return craftingGridSizeSmall
}

// craftingOffset gets the crafting offset based on the opened container ID.
func (s *Session) craftingOffset() uint32 {
	if s.openedContainerID.Load() == 1 {
		return craftingGridLargeOffset
	}
	return craftingGridSmallOffset
}

// duplicateStack duplicates an item.Stack with the new item type given.
func duplicateStack(input item.Stack, newType world.Item) item.Stack {
	outputStack := item.NewStack(newType, input.Count()).
		Damage(input.MaxDurability() - input.Durability()).
		WithCustomName(input.CustomName()).
		WithLore(input.Lore()...).
		WithEnchantments(input.Enchantments()...).
		WithAnvilCost(input.AnvilCost())
	for k, v := range input.Values() {
		outputStack = outputStack.WithValue(k, v)
	}
	return outputStack
}

// matchingStacks returns true if the two stacks are the same in a crafting scenario.
func matchingStacks(has, expected item.Stack) bool {
	_, variants := expected.Value("variants")
	if !variants {
		return has.Comparable(expected)
	}
	nameOne, _ := has.Item().EncodeItem()
	nameTwo, _ := expected.Item().EncodeItem()
	return nameOne == nameTwo
}
