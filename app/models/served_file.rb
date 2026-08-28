class ServedFile < ApplicationRecord
  FILES_DIR = ResolvesServedFiles::FILES_DIR
  ALLOWED_EXTENSIONS = ResolvesServedFiles::ALLOWED_EXTENSIONS

  validates :name, presence: true, uniqueness: true

  def self.sync!
    disk_names = FILES_DIR.children
      .select { |path| path.file? && ALLOWED_EXTENSIONS.include?(path.extname.downcase) }
      .map { |path| path.basename.to_s }

    now = Time.current
    rows = disk_names.map { |name| { name: name, created_at: now, updated_at: now } }
    insert_all(rows, unique_by: :name) if rows.any?
    where.not(name: disk_names).delete_all
  end

  def self.record(name)
    return unless ALLOWED_EXTENSIONS.include?(File.extname(name).downcase)

    now = Time.current
    insert_all([{ name: name, created_at: now, updated_at: now }], unique_by: :name)
  end

  def self.remove(name)
    where(name: name).delete_all
  end

  def self.record_modification(name)
    find_or_create_by!(name: name).touch
  rescue ActiveRecord::RecordNotUnique
    find_by!(name: name).touch
  end

  def self.newest
    order(created_at: :desc, id: :desc).first
  end
end
